// gateway/main.go
package main

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"

	imetrics "hitl-platform/internal/metrics"
)

// ── Config ────────────────────────────────────────────────────────────────────

var (
	backendURL  = envOr("BACKEND_URL", "http://localhost:8000")
	corsOrigins = strings.Split(envOr("CORS_ORIGINS", "http://localhost:5173"), ",")
	gatewayPort = envOr("GATEWAY_PORT", "9000")
	redisAddr   = envOr("REDIS_HOST", "localhost") + ":6379"
	exposeDocs  = os.Getenv("EXPOSE_DOCS") == "true"
)

// Size limits to prevent memory exhaustion
const (
	maxRequestBody  = 10 * 1024 * 1024 // 10 MB
	maxResponseBody = 50 * 1024 * 1024 // 50 MB
)

// Paths that bypass JWT verification
var publicPaths = map[string]bool{
	"/api/auth/login":                 true,
	"/api/auth/register":              true,
	"/api/auth/forgot-password":       true,
	"/api/auth/reset-password":        true,
	"/api/auth/oauth/google":          true,
	"/api/auth/oauth/google/callback": true,
	"/api/auth/oauth/session":         true,
	"/health":                         true,
	"/api/payment/calculate":          true,
}

// Prefixes that bypass JWT
var publicPrefixes = []string{"/api/account/", "/api/payment/webhook/"}

// ── Redis ─────────────────────────────────────────────────────────────────────

var rdb *redis.Client

func initRedis() {
	rdb = redis.NewClient(&redis.Options{Addr: redisAddr, DB: 0})
}

func tokenKey(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("blacklist:token:%x", h[:])
}

func userFlagKey(userID string) string {
	return "blacklist:user_flag:" + userID
}

func isBlacklisted(ctx context.Context, rawToken, userID string) bool {
	n, err := rdb.Exists(ctx, tokenKey(rawToken), userFlagKey(userID)).Result()
	if err != nil {
		slog.Warn("redis blacklist check failed (failing open)", slog.Any("error", err))
		return false
	}
	return n > 0
}

// ── Rate limiting ─────────────────────────────────────────────────────────────

func checkRateLimit(userID string) error {
	// Per-user sliding window: 100 requests per minute
	key := fmt.Sprintf("ratelimit:%s", userID)
	now := time.Now().Unix()
	window := int64(60) // 1 minute

	pipe := rdb.Pipeline()
	zremrange := pipe.ZRemRangeByScore(context.Background(), key, "0", fmt.Sprintf("%d", now-window))
	zcard := pipe.ZCard(context.Background(), key)
	zadd := pipe.ZAdd(context.Background(), key, redis.Z{Score: float64(now), Member: now})
	expire := pipe.Expire(context.Background(), key, time.Duration(window)*time.Second)
	_, err := pipe.Exec(context.Background())

	if err != nil {
		slog.Warn("rate limiter redis error (failing open)", slog.Any("error", err))
		return nil // fail open
	}

	_ = zremrange
	_ = zadd
	_ = expire

	count := zcard.Val()
	if count > 100 {
		return fmt.Errorf("rate limit exceeded")
	}
	return nil
}

// ── Public key loading (RS256) ───────────────────────────────────────────────

var publicKey *rsa.PublicKey

// loadPublicKey reads the RSA public key — safe to ship with every desktop
// install. The public key can verify signatures but cannot forge new ones.
func loadPublicKey() (*rsa.PublicKey, error) {
	if publicKey != nil {
		return publicKey, nil
	}
	path := os.Getenv("JWT_PUBLIC_KEY_PATH")
	if path == "" {
		return nil, fmt.Errorf("JWT_PUBLIC_KEY_PATH not set")
	}
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read public key at %s: %w", path, err)
	}
	key, err := jwt.ParseRSAPublicKeyFromPEM(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("could not parse public key: %w", err)
	}
	publicKey = key
	return publicKey, nil
}

// ── JWT verification ──────────────────────────────────────────────────────────

type claims struct {
	Sub      string `json:"sub"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

func verifyJWT(authHeader string) (*claims, string, error) {
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return nil, "", fmt.Errorf("missing or malformed Authorization header")
	}
	raw := strings.TrimPrefix(authHeader, "Bearer ")

	key, err := loadPublicKey()
	if err != nil {
		return nil, "", fmt.Errorf("cannot verify tokens: %w", err)
	}

	token, err := jwt.ParseWithClaims(raw, &claims{},
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return key, nil
		})
	if err != nil {
		return nil, "", err
	}
	c, ok := token.Claims.(*claims)
	if !ok || !token.Valid {
		return nil, "", fmt.Errorf("invalid token")
	}
	return c, raw, nil
}

// ── Structured error response ─────────────────────────────────────────────────

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error":  code,
		"detail": message,
	})
}

// ── Reverse proxy ─────────────────────────────────────────────────────────────

var httpClient = &http.Client{Timeout: 60 * time.Second}

func proxy(w http.ResponseWriter, r *http.Request, extraHeaders map[string]string) {
	start := time.Now()
	target, _ := url.Parse(backendURL)
	target.Path = r.URL.Path
	target.RawQuery = r.URL.RawQuery

	// Read body with size limit
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		slog.Error("failed to read request body", slog.Any("error", err))
		writeError(w, http.StatusBadRequest, "bad_request", "Failed to read request body")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(),
		strings.NewReader(string(bodyBytes)))
	if err != nil {
		slog.Error("failed to build upstream request", slog.Any("error", err))
		writeError(w, http.StatusBadGateway, "proxy_error", "Failed to build upstream request")
		return
	}

	// Copy original headers (strip hop-by-hop)
	hopByHop := map[string]bool{
		"connection": true, "transfer-encoding": true,
		"te": true, "trailer": true, "upgrade": true, "host": true,
	}
	for k, vals := range r.Header {
		if !hopByHop[strings.ToLower(k)] {
			for _, v := range vals {
				req.Header.Add(k, v)
			}
		}
	}

	// Inject trusted headers from JWT verification
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	if r.RemoteAddr != "" {
		req.Header.Set("X-Forwarded-For", r.RemoteAddr)
	}
	req.Header.Set("X-Forwarded-Proto", "http")

	// Carry the trace id to the backend so both hops of the same request
	// share one id in the logs. imetrics.Middleware put it on the context
	// and echoed it to the client already.
	if traceID := imetrics.TraceID(r.Context()); traceID != "" {
		req.Header.Set(imetrics.TraceHeader, traceID)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "connection refused") {
			writeError(w, http.StatusServiceUnavailable, "backend_unavailable", "Backend service unavailable")
			return
		}
		writeError(w, http.StatusGatewayTimeout, "gateway_timeout", "Backend request timed out")
		return
	}
	defer resp.Body.Close()

	// Read response with size limit
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		slog.Error("failed to read response", slog.Any("error", err))
		writeError(w, http.StatusBadGateway, "read_error", "Failed to read upstream response")
		return
	}

	// Copy response headers
	skipResp := map[string]bool{
		"transfer-encoding": true, "connection": true, "content-encoding": true,
	}
	for k, vals := range resp.Header {
		if !skipResp[strings.ToLower(k)] {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(respBytes)

	// Structured log after response
	duration := time.Since(start)
	userID := extraHeaders["X-User-Id"]
	if userID == "" {
		userID = "anonymous"
	}
	slog.Info("gateway relay complete",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("user_id", userID),
		slog.String("role", extraHeaders["X-User-Role"]),
		slog.Int("upstream_status", resp.StatusCode),
		slog.Duration("duration", duration),
		slog.Int64("request_bytes", int64(len(bodyBytes))),
		slog.Int64("response_bytes", int64(len(respBytes))),
	)
}

// ── Main handler ──────────────────────────────────────────────────────────────

func gatewayHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	path := r.URL.Path

	// Check if public
	isPublic := publicPaths[path]
	if !isPublic {
		for _, prefix := range publicPrefixes {
			if strings.HasPrefix(path, prefix) {
				isPublic = true
				break
			}
		}
	}
	if exposeDocs && (path == "/docs" || path == "/openapi.json" || path == "/redoc") {
		isPublic = true
	}

	if isPublic {
		slog.Info("public request", slog.String("method", r.Method), slog.String("path", path))
		proxy(w, r, nil)
		return
	}

	// Verify JWT
	c, rawToken, err := verifyJWT(r.Header.Get("Authorization"))
	if err != nil {
		slog.Warn("jwt verification failed",
			slog.String("path", path),
			slog.String("error", err.Error()),
		)
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}

	// Rate limit check
	if err := checkRateLimit(c.Sub); err != nil {
		slog.Warn("rate limit exceeded",
			slog.String("user_id", c.Sub),
			slog.String("path", path),
		)
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests — slow down")
		return
	}

	// Blacklist check
	if isBlacklisted(r.Context(), rawToken, c.Sub) {
		slog.Warn("blacklisted token used",
			slog.String("user_id", c.Sub),
			slog.String("path", path),
		)
		writeError(w, http.StatusUnauthorized, "account_deactivated", "Account has been deactivated. Contact an administrator.")
		return
	}

	// Compute token hash + exp for backend
	h := sha256.Sum256([]byte(rawToken))
	tokHash := fmt.Sprintf("%x", h[:])
	tokExp := ""
	if c.ExpiresAt != nil {
		tokExp = fmt.Sprintf("%d", c.ExpiresAt.Unix())
	}

	duration := time.Since(start)
	slog.Info("authenticated request",
		slog.String("method", r.Method),
		slog.String("path", path),
		slog.String("user_id", c.Sub),
		slog.String("role", c.Role),
		slog.Duration("auth_duration", duration),
	)

	proxy(w, r, map[string]string{
		"X-User-Id":    c.Sub,
		"X-User-Role":  c.Role,
		"X-Token-Exp":  tokExp,
		"X-Token-Hash": tokHash,
	})
}

// ── Entry point ───────────────────────────────────────────────────────────────

func main() {
	// Structured JSON logging to stdout
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	initRedis()
	slog.Info("gateway started", slog.String("backend", backendURL), slog.String("port", gatewayPort))

	imetrics.SetBuildInfo("gateway", os.Getenv("APP_VERSION"))

	// The gateway proxies paths it has no routes for, so chi's
	// RoutePattern is always "/*" here. Leave RouteTemplateFunc unset so
	// internal/metrics falls back to its path-templating heuristic, which
	// collapses ids and gives per-route labels that actually distinguish
	// endpoints.
	imetrics.StartHealthProbes(context.Background(), 15*time.Second,
		imetrics.HealthProbe{Name: "gateway", Kind: "redis", Check: func(ctx context.Context) error {
			return rdb.Ping(ctx).Err()
		}},
	)

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	// Before auth, so rejected requests are still counted.
	r.Use(imetrics.Middleware("gateway"))
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   corsOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Trace-Id"},
		ExposedHeaders:   []string{"X-Trace-Id"},
		AllowCredentials: true,
	}))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"gateway"}`))
	})

	// Scrape target for the gateway's own traffic. This is the edge, so
	// these numbers include requests the backend never sees — rejected
	// JWTs, rate-limited callers, blacklisted tokens. The gap between
	// gateway and server request counts is exactly the traffic the
	// gateway is turning away, which is worth being able to graph.
	r.Handle(imetrics.MetricsPath(), imetrics.Handler())

	r.HandleFunc("/*", gatewayHandler)

	srv := &http.Server{
		Addr:         ":" + gatewayPort,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		slog.Info("listening", slog.String("addr", ":"+gatewayPort))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("listen failed", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	slog.Info("shutdown signal received", slog.String("signal", sig.String()))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("forced shutdown", slog.Any("error", err))
	}
	if rdb != nil {
		rdb.Close()
	}
	slog.Info("gateway exited cleanly")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
