// internal/metrics/metrics.go
//
// Every metric the Go side of the system exposes, defined in one place so
// the metric surface can be read top to bottom rather than discovered by
// grepping for NewCounter.
//
// # NAMING
//
// All series are prefixed `bondqi_`. Durations are in SECONDS, not
// milliseconds — Prometheus convention is base units, and Grafana will
// render seconds as "340 ms" on its own. Counters end in `_total`. These
// are not stylistic preferences; PromQL functions and Grafana's unit
// auto-detection both key off them.
//
// # CARDINALITY
//
// Every label here is bounded to a small set of values on purpose. The
// single fastest way to take down a Prometheus server is a label whose
// value comes from user input — a user id, a document id, an extraction
// id, a raw URL path with an id in it. One series per user is not
// monitoring, it is a memory leak with a graph attached. That is why
// middleware.go templates `/api/review/9f3c-...` down to
// `/api/review/{id}` before it ever reaches a label, and why error labels
// carry a classified `kind` rather than err.Error().
package metrics

import (
	"os"
	"runtime"
	"time"
)

// ── Build / process ──────────────────────────────────────────────────────────

var (
	// BuildInfo is the standard always-1 gauge whose labels carry the
	// version. You join against it in PromQL to annotate any other series
	// with the version that produced it.
	BuildInfo = NewGauge("bondqi_build_info",
		"Always 1. Labels carry build and service identity.",
		"service", "version", "go_version")

	processStart = time.Now()
)

// ── HTTP ─────────────────────────────────────────────────────────────────────

var (
	HTTPRequests = NewCounter("bondqi_http_requests_total",
		"Total HTTP requests handled, by templated route and status class.",
		"service", "method", "route", "status")

	HTTPDuration = NewHistogram("bondqi_http_request_duration_seconds",
		"HTTP request latency in seconds, measured from first byte in to last byte out.",
		DefaultBuckets, "service", "method", "route")

	HTTPInFlight = NewGauge("bondqi_http_requests_in_flight",
		"Requests currently being served. A rising floor here means work is piling up, not just arriving.",
		"service")

	HTTPResponseSize = NewHistogram("bondqi_http_response_size_bytes",
		"Response body size in bytes.",
		[]float64{256, 1024, 8192, 65536, 524288, 4194304, 33554432},
		"service", "route")

	HTTPErrors = NewCounter("bondqi_http_errors_total",
		"Requests that failed, split by whether the fault was the client's or ours.",
		"service", "route", "kind") // kind: client_error | server_error
)

// ── Database ─────────────────────────────────────────────────────────────────

var (
	DBQueries = NewCounter("bondqi_db_queries_total",
		"Database queries executed, by logical operation and outcome.",
		"op", "table", "status") // status: ok | error

	DBDuration = NewHistogram("bondqi_db_query_duration_seconds",
		"Time spent inside a database query, including waiting for a pooled connection.",
		DefaultBuckets, "op", "table")

	DBErrors = NewCounter("bondqi_db_errors_total",
		"Failed database queries, by classified error kind.",
		"op", "table", "kind")

	DBUp = NewGauge("bondqi_db_up",
		"1 when the last Postgres ping succeeded, 0 when it failed.",
		"database")

	DBPingDuration = NewHistogram("bondqi_db_ping_duration_seconds",
		"Round-trip time of the health-check ping.",
		DefaultBuckets, "database")
)

// ── Redis ────────────────────────────────────────────────────────────────────

var (
	RedisUp = NewGauge("bondqi_redis_up",
		"1 when the last Redis ping succeeded, 0 when it failed.",
		"instance")

	RedisCommands = NewCounter("bondqi_redis_commands_total",
		"Redis commands issued, by command and outcome.",
		"command", "status")

	RedisDuration = NewHistogram("bondqi_redis_command_duration_seconds",
		"Redis command round-trip time.",
		DefaultBuckets, "command")
)

// ── Generic spans: instrument ANY part of the code ───────────────────────────
//
// This is the escape hatch that makes the rest of the system measurable
// without inventing a new metric family for every function. Wrap anything
// — a parser, a template match, an S3 upload, a third-party call — in
// metrics.Span and it lands in these three families, queryable by
// component and operation. See trace.go for the wrapper.

var (
	SpanDuration = NewHistogram("bondqi_span_duration_seconds",
		"Duration of an instrumented code span, by component and operation.",
		LLMBuckets, "component", "operation", "status")

	SpanTotal = NewCounter("bondqi_spans_total",
		"Instrumented code spans completed, by component, operation and outcome.",
		"component", "operation", "status")

	SpanErrors = NewCounter("bondqi_span_errors_total",
		"Instrumented code spans that returned an error, by classified error kind.",
		"component", "operation", "kind")

	SpanInFlight = NewGauge("bondqi_spans_in_flight",
		"Instrumented spans currently executing.",
		"component", "operation")
)

// ── Extraction pipeline / queue ──────────────────────────────────────────────

var (
	QueueDepth = NewGauge("bondqi_queue_depth",
		"Extraction jobs in pending or processing state.",
		"queue")

	SLABreaches = NewGauge("bondqi_sla_breaches",
		"Jobs past their SLA deadline that have not completed.",
		"queue")

	ExtractionJobs = NewCounter("bondqi_extraction_jobs_total",
		"Extraction jobs by terminal status.",
		"status", "source") // status: completed | failed | escalated

	ExtractionDuration = NewHistogram("bondqi_extraction_duration_seconds",
		"Wall-clock time from job creation to completion.",
		LLMBuckets, "source")

	ReviewQueueDepth = NewGauge("bondqi_review_queue_depth",
		"Items waiting in the human review queue.",
		"queue")
)

// ── Payments ─────────────────────────────────────────────────────────────────

var (
	PaymentAttempts = NewCounter("bondqi_payment_attempts_total",
		"Payment charge attempts, by gateway, plan and outcome.",
		"gateway", "plan", "status") // status: succeeded | pending | failed

	PaymentDuration = NewHistogram("bondqi_payment_duration_seconds",
		"Time spent inside the payment gateway's Charge call.",
		LLMBuckets, "gateway")

	PaymentAmount = NewCounter("bondqi_payment_amount_total",
		"Gross charged amount, in currency units.",
		"gateway", "currency")

	TokensGranted = NewCounter("bondqi_tokens_granted_total",
		"Field credits added to user balances, by what caused the grant.",
		"reason") // reason: purchase | free_claim | coupon | admin
)

// ── Coupons ──────────────────────────────────────────────────────────────────

var (
	CouponsCreated = NewCounter("bondqi_coupons_created_total",
		"Coupons generated, by kind and whether they were made singly or in a batch.",
		"kind", "mode") // mode: single | bulk

	CouponRedemptions = NewCounter("bondqi_coupon_redemptions_total",
		"Coupon redemption attempts, by kind and outcome.",
		"kind", "result") // result: accepted | rejected

	CouponRejections = NewCounter("bondqi_coupon_rejections_total",
		"Rejected coupon attempts, by reason. A spike in not_found usually means someone is guessing codes.",
		"reason")

	CouponDiscount = NewCounter("bondqi_coupon_discount_amount_total",
		"Total discount given away through coupons, in currency units.",
		"currency")

	CouponTokensGranted = NewCounter("bondqi_coupon_tokens_granted_total",
		"Field credits granted through token-kind coupons.")

	CouponsActive = NewGauge("bondqi_coupons_active",
		"Coupons currently active and not expired.")
)

// ── Auth ─────────────────────────────────────────────────────────────────────

var (
	AuthAttempts = NewCounter("bondqi_auth_attempts_total",
		"Authentication attempts by outcome.",
		"result") // result: success | bad_credentials | locked | captcha_required | otp_required

	TokensIssued = NewCounter("bondqi_auth_tokens_issued_total",
		"Access tokens issued.",
		"kind") // kind: password | oauth | refresh
)

// ── Email ────────────────────────────────────────────────────────────────────

var (
	EmailsSent = NewCounter("bondqi_emails_sent_total",
		"Outbound emails, by purpose, provider and outcome.",
		"kind", "provider", "result")

	EmailDuration = NewHistogram("bondqi_email_send_duration_seconds",
		"Time spent handing an email to the provider.",
		LLMBuckets, "provider")
)

// ── Alerting (the alert service monitoring itself) ───────────────────────────
//
// An alert engine that fails silently is worse than no alert engine, because
// it converts "we have no monitoring" into "we believe we have monitoring".
// These series exist so the engine's own health is visible, and so you can
// set a Prometheus-side alert on bondqi_alert_evaluation_errors_total —
// the one alert that cannot rely on this engine working.

var (
	AlertRulesActive = NewGauge("bondqi_alert_rules_active",
		"Alert rules currently enabled.")

	AlertsFiring = NewGauge("bondqi_alerts_firing",
		"Alerts currently in the firing state, by severity.",
		"severity")

	AlertEvaluations = NewCounter("bondqi_alert_evaluations_total",
		"Rule evaluations performed, by rule and result.",
		"rule", "result") // result: ok | breaching | error

	AlertEvaluationDuration = NewHistogram("bondqi_alert_evaluation_duration_seconds",
		"Time to evaluate one full pass over all rules.",
		DefaultBuckets)

	AlertEvaluationErrors = NewCounter("bondqi_alert_evaluation_errors_total",
		"Rule evaluations that could not compute a value.",
		"rule", "metric")

	AlertNotifications = NewCounter("bondqi_alert_notifications_total",
		"Alert emails dispatched, by severity and outcome.",
		"severity", "result") // result: sent | failed | no_recipients | suppressed
)

// ── Go runtime ───────────────────────────────────────────────────────────────
//
// Registered as GaugeFuncs so they read live at scrape time and nothing
// has to run a background sampler. Deliberately a short list: goroutines
// and heap catch the leaks that actually happen in this codebase (a
// handler that forgets to cancel a context, a fire-and-forget goroutine
// that blocks forever on a dead database).

func init() {
	NewGaugeFunc("bondqi_go_goroutines",
		"Goroutines currently running. A number that only ever rises is a leak.",
		func() float64 { return float64(runtime.NumGoroutine()) })

	NewGaugeFunc("bondqi_go_heap_alloc_bytes",
		"Heap memory currently allocated and in use.",
		func() float64 {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			return float64(m.HeapAlloc)
		})

	NewGaugeFunc("bondqi_go_heap_objects",
		"Live objects on the heap.",
		func() float64 {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			return float64(m.HeapObjects)
		})

	NewGaugeFunc("bondqi_go_gc_pause_seconds_total",
		"Cumulative stop-the-world GC pause time.",
		func() float64 {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			return float64(m.PauseTotalNs) / 1e9
		})

	NewGaugeFunc("bondqi_process_uptime_seconds",
		"Seconds since this process started.",
		func() float64 { return time.Since(processStart).Seconds() })

	NewGaugeFunc("bondqi_process_start_time_seconds",
		"Unix timestamp of process start. Use this to detect restarts.",
		func() float64 { return float64(processStart.Unix()) })
}

// SetBuildInfo records service identity. Call once at startup, before the
// first scrape, from each binary (server and gateway) with its own name.
func SetBuildInfo(service, version string) {
	if version == "" {
		version = envOr("APP_VERSION", "dev")
	}
	BuildInfo.With(service, version, runtime.Version()).Set(1)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
