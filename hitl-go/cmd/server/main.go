// cmd/server/main.go
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	ialerting "hitl-platform/internal/alerting"
	ibackup "hitl-platform/internal/backup"
	icoupon "hitl-platform/internal/coupon"
	idb "hitl-platform/internal/db"
	imetrics "hitl-platform/internal/metrics"
	imw "hitl-platform/internal/middleware"
	ipayment "hitl-platform/internal/payment"
	iplanstore "hitl-platform/internal/planstore"
	iqueue "hitl-platform/internal/queue"
	ireviewqueue "hitl-platform/internal/reviewqueue"
	"hitl-platform/routers"
)

func main() {
	logger := log.New(os.Stdout, "[HITL-SERVER] ", log.LstdFlags)

	// ── Startup ───────────────────────────────────────────────────────────────
	// Cancellable so the health probes and the alert evaluation loop stop
	// when the process shuts down, rather than being killed mid-query.
	ctx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()

	logger.Println("=== Server starting up ===")

	// Identify this process on every metric and log line.
	imetrics.SetBuildInfo("hitl-server", os.Getenv("APP_VERSION"))

	// 1. Postgres connection pool
	if err := idb.Init(ctx); err != nil {
		logger.Fatalf("DB init failed: %v", err)
	}
	logger.Println("Postgres connected")

	// 2. Redis clients (Celery broker + blacklist)
	iqueue.Init()
	iqueue.InitBlacklist()
	logger.Println("Redis connected")

	// 2b. Review-queue client (separate Redis DB from the Celery broker —
	// see internal/reviewqueue's doc comment for why this is its own
	// small client rather than reusing iqueue's).
	ireviewqueue.Init()
	logger.Println("Review queue client ready")

	// 3. Plan transactions store (Postgres by default, MySQL via DB_DRIVER=mysql)
	//    + payment gateway (mock by default, "stripe" via PAYMENT_GATEWAY once
	//    StripeGateway.Charge is filled in).
	txStore, err := iplanstore.NewStore(os.Getenv("DB_DRIVER"), idb.Pool(), nil)
	if err != nil {
		logger.Fatalf("planstore init failed: %v", err)
	}
	if err := txStore.EnsureSchema(ctx); err != nil {
		logger.Fatalf("planstore schema failed: %v", err)
	}
	routers.InitPayment(ipayment.NewGateway(os.Getenv("PAYMENT_GATEWAY")), txStore)
	logger.Println("Payment gateway + plan_transactions store ready")

	// 4. Coupon service
	routers.InitCoupons(icoupon.NewStore(idb.Pool()))
	logger.Println("Coupon service ready")

	// 5. Observability: connection pool gauges and background health
	//    probes.
	//
	//    Health is sampled on a ticker rather than computed during a
	//    /metrics scrape on purpose. A scrape-time ping would block the
	//    whole response for the connect timeout whenever Postgres was
	//    down — so the scrape would fail at exactly the moment the
	//    bondqi_db_up=0 sample needs to reach Prometheus and fire the
	//    alert.
	imetrics.RegisterPoolCollector("main", func() imetrics.PoolStats {
		st := idb.Pool().Stat()
		return imetrics.PoolStats{
			Acquired:      st.AcquiredConns(),
			Idle:          st.IdleConns(),
			Total:         st.TotalConns(),
			Max:           st.MaxConns(),
			AcquireCount:  st.AcquireCount(),
			EmptyAcquires: st.EmptyAcquireCount(),
		}
	})

	imetrics.StartHealthProbes(ctx, 15*time.Second,
		imetrics.HealthProbe{Name: "main", Kind: "db", Check: idb.PingDB},
		imetrics.HealthProbe{Name: "main", Kind: "redis", Check: iqueue.Ping},
	)
	logger.Println("Metrics + health probes running")

	// Reclaims expired "monthly"/"annual" purchase credits (see
	// migrations/0005_token_grants.sql and routers/payment.go's plans
	// map) — without this running, nothing ever actually removes an
	// expired grant's fields from a user's balance; they'd just sit
	// there indefinitely despite having a passed expires_at.
	idb.StartTokenGrantSweep(ctx, time.Hour)
	logger.Println("Token grant expiry sweep running")

	// 6. Alert service. Started before the HTTP server so a startup
	//    problem shows up in the logs next to everything else, and so a
	//    database that is already down is alerted on immediately rather
	//    than 30 seconds after the first request.
	alertStore := ialerting.NewStore(idb.Pool())
	alertNotifier := &ialerting.EmailNotifier{
		AppName:      envOr("APP_NAME", "CheckMyBond"),
		DashboardURL: os.Getenv("GRAFANA_DASHBOARD_URL"),
	}
	alertEngine := ialerting.NewEngine(alertStore, idb.Pool(), alertNotifier,
		durationFromEnv("ALERT_EVAL_INTERVAL_SECONDS", 30*time.Second))
	alertEngine.Start(ctx)
	routers.InitAlerts(alertStore, alertEngine)
	logger.Println("Alert service running")

	// ── Router ────────────────────────────────────────────────────────────────
	r := chi.NewRouter()

	// Route labels come from chi's own matched pattern rather than the
	// heuristic in internal/metrics — exact instead of guessed, and it
	// means a new route with an id in it is labelled correctly with no
	// change to the templating rules.
	imetrics.RouteTemplateFunc = func(req *http.Request) string {
		if rc := chi.RouteContext(req.Context()); rc != nil {
			return rc.RoutePattern()
		}
		return ""
	}

	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	// Metrics go BEFORE auth and the audit log so that requests rejected
	// for a missing identity still appear in the numbers — a flood of
	// 401s is the signature of credential stuffing, and instrumenting
	// after the auth layer would make it invisible.
	r.Use(imetrics.Middleware("hitl-server"))
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(60 * time.Second))
	r.Use(imw.AuditLog) // audit every request

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type",
			"X-User-Id", "X-User-Role", "X-Token-Exp", "X-Token-Hash"},
		AllowCredentials: true,
	}))

	// Public health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"hitl-server"}`))
	})

	// Readiness: reports whether dependencies are actually usable, not
	// just whether the process is alive. /health answering while Postgres
	// is unreachable is how a broken instance stays in a load balancer
	// pool.
	r.Get("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := idb.PingDB(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"degraded","database":"unreachable"}`))
			return
		}
		w.Write([]byte(`{"status":"ready"}`))
	})

	// Prometheus scrape target. Guard it with METRICS_TOKEN or
	// METRICS_ALLOW_CIDR, or keep the port off the public interface —
	// /metrics exposes route names, error rates and revenue counters.
	r.Handle(imetrics.MetricsPath(), imetrics.Handler())

	// Route mounts
	r.Mount("/api/auth", routers.AuthRouter())
	r.Mount("/api/documents", routers.DocumentsRouter())
	r.Mount("/api/extractions", routers.ExtractionsRouter())
	r.Mount("/api/review", routers.ReviewRouter())
	r.Mount("/api/queue", routers.QueueRouter())
	r.Mount("/api/monitoring", routers.MonitoringRouter())
	r.Mount("/api/account", routers.AccountRouter())
	r.Mount("/api/hitl", routers.HITLRouter())
	r.Mount("/api/performance", routers.PerformanceRouter())

	// Payment (routers/payment.go, routers/payment_webhooks.go).
	// /calculate and /webhook/* are public inside PaymentRouter itself;
	// /pay and /verify require imw.Identity. The gateway's own
	// publicPaths/publicPrefixes (hitl-go/gateway/main.go) must list
	// /api/payment/calculate and /api/payment/webhook/ too, or the gateway
	// rejects those requests for missing a JWT before they ever get here.
	r.Mount("/api/payment", routers.PaymentRouter())

	// User self-service — feedback/complaints and account deletion
	// (routers/profile.go). All routes require imw.Identity.
	r.Mount("/api/profile", routers.ProfileRouter())

	// Google sign-in / auto-registration (routers/google_oauth.go)
	// Kept under its own "/oauth" segment so it can't collide with
	// whatever AuthRouter() already serves under /api/auth.
	r.Get("/api/auth/oauth/google", routers.GoogleLoginHandler)
	r.Get("/api/auth/oauth/google/callback", routers.GoogleCallbackHandler)
	r.Get("/api/auth/oauth/session", routers.GoogleSessionHandler)

	// DESKTOP-for listning from desktop user
	r.Mount("/api/desktop", DesktopRouter())
	r.Mount("/api/templates", routers.TemplatesRouter())
	r.Mount("/api/fields", routers.FieldsRouter())
	r.Mount("/api/reconcile", routers.ReconcileRouter())
	r.Mount("/api/schemas", routers.SchemasRouter())
	r.Mount("/api/contact", routers.ContactRouter())

	// Coupon service — generation (admin) and redemption (any signed-in
	// user). Every route requires a verified identity, so no gateway
	// publicPaths change is needed.
	r.Mount("/api/coupons", routers.CouponRouter())

	// Alert service admin — rules, recipients, incident history.
	r.Mount("/api/alerts", routers.AlertsRouter())
	// ── HTTP server ───────────────────────────────────────────────────────────
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		logger.Printf("Listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("ListenAndServe: %v", err)
		}
	}()

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	logger.Printf("=== Shutdown signal received: %s ===", sig)

	// Stop the alert loop and health probes before draining, so they are
	// not issuing queries against a pool that is about to close.
	stopBackground()

	logger.Println("Draining active connections (up to 30 s)...")

	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Fatalf("Forced shutdown: %v", err)
	}

	// Backup DB state before exit
	logger.Println("Backing up DB state...")
	backupCtx, backupCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer backupCancel()
	ibackup.Run(backupCtx)

	// Close connections
	idb.Close()
	iqueue.Close()
	ireviewqueue.Close()

	logger.Println("=== Server exited cleanly ===")
}

// envOr returns the environment variable or a fallback.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// durationFromEnv reads a whole number of seconds from the environment.
// An unset or unparseable value falls back to def rather than failing
// startup — a mistyped tuning knob should not stop the server booting.
func durationFromEnv(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return time.Duration(n) * time.Second
}
