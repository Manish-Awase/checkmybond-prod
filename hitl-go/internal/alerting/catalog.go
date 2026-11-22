// internal/alerting/catalog.go
//
// The catalog of quantities an alert rule can be written against.
//
// # WHY A CATALOG INSTEAD OF FREE-FORM QUERIES
//
// alert_rules.metric is a key into this map, not a SQL fragment. An admin
// UI writes to that table, so if the column held SQL, the alert
// configuration screen would be a remote code execution endpoint wearing
// a monitoring costume. A fixed catalog means the worst a compromised
// admin account can do to this subsystem is create a noisy alert.
//
// It also keeps the UI honest: GET /api/alerts/catalog serves this exact
// list, so the metric dropdown cannot offer something the engine has no
// idea how to compute.
//
// # WHERE THE NUMBERS COME FROM
//
// Two sources, chosen per metric:
//
//	SQL     — durable, shared across every replica, survives a restart.
//	          Used wherever the data already lands in Postgres: audit_log
//	          records every request with its duration and status, so HTTP
//	          error rate and latency are best read from there.
//	Process — the in-memory rolling windows and gauges from
//	          internal/metrics. Used for things Postgres never sees:
//	          how long queries themselves took, whether Redis answered,
//	          goroutine counts.
//
// The split matters during an incident. If Postgres is down, SQL-backed
// resolvers fail — which is correct, and the process-backed db_up rule
// fires instead and tells you exactly that.
package alerting

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	imetrics "hitl-platform/internal/metrics"
)

// ResolveContext is what a resolver gets to work with.
type ResolveContext struct {
	Ctx    context.Context
	Pool   *pgxpool.Pool
	Window time.Duration
	Labels map[string]string
}

// Label reads an optional narrowing label, with a fallback.
func (rc ResolveContext) Label(key, def string) string {
	if v, ok := rc.Labels[key]; ok && v != "" {
		return v
	}
	return def
}

// Resolver computes one alertable quantity.
type Resolver struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`       // human name for the UI dropdown
	Description string  `json:"description"` // what the number means
	Unit        string  `json:"unit"`        // percent | ms | seconds | count | per_min | bool | ratio
	Suggested   float64 `json:"suggestedThreshold"`
	Comparator  string  `json:"suggestedComparator"`
	Source      string  `json:"source"` // sql | process

	Fn func(ResolveContext) (float64, error) `json:"-"`
}

// Catalog is keyed by alert_rules.metric.
var Catalog = map[string]Resolver{}

func register(r Resolver) { Catalog[r.Key] = r }

// CatalogList returns the catalog sorted by key, for the API.
func CatalogList() []Resolver {
	out := make([]Resolver, 0, len(Catalog))
	for _, r := range Catalog {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// pct is the share of `part` in `total`, as a percentage, with an empty
// total treated as 0%.
//
// Returning 0 rather than NaN for "no traffic" is a deliberate choice. A
// NaN compared against any threshold is false, so an error-rate rule
// would silently stop evaluating overnight when traffic drops to zero —
// and then not fire on the first error of the morning either, because
// one error out of one request is 100% and would page someone over a
// single failed health check. Treating no traffic as 0% keeps the rule
// quiet when nothing is happening, which is the honest answer.
func pct(part, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return part / total * 100
}

func init() {
	// ── HTTP, from audit_log ────────────────────────────────────────────────

	register(Resolver{
		Key: "http_error_rate_pct", Label: "API error rate",
		Description: "Share of requests returning 5xx, across every route.",
		Unit:        "percent", Suggested: 5, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var total, errs float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT count(*)::float8,
				       count(*) FILTER (WHERE status_code >= 500)::float8
				FROM audit_log WHERE timestamp >= now() - $1::interval`,
				intervalArg(rc.Window)).Scan(&total, &errs)
			if err != nil {
				return 0, err
			}
			return pct(errs, total), nil
		},
	})

	register(Resolver{
		Key: "http_latency_p95_ms", Label: "API p95 latency",
		Description: "95th percentile request duration across every route.",
		Unit:        "ms", Suggested: 2000, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var p95 *float64
			// percentile_disc picks an actual observed value rather than
			// interpolating between two, which is what you want for a
			// latency SLO: the number reported is a request that really
			// took that long.
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT percentile_disc(0.95) WITHIN GROUP (ORDER BY duration_ms)::float8
				FROM audit_log WHERE timestamp >= now() - $1::interval`,
				intervalArg(rc.Window)).Scan(&p95)
			if err != nil {
				return 0, err
			}
			if p95 == nil {
				return 0, nil
			}
			return *p95, nil
		},
	})

	register(Resolver{
		Key: "http_route_latency_p95_ms", Label: "Route p95 latency",
		Description: "95th percentile duration for one route. Set labels to {\"route\":\"/api/documents/upload\"}.",
		Unit:        "ms", Suggested: 5000, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			route := rc.Label("route", "")
			if route == "" {
				return 0, fmt.Errorf("this rule needs a route label, e.g. {\"route\":\"/api/documents/upload\"}")
			}
			var p95 *float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT percentile_disc(0.95) WITHIN GROUP (ORDER BY duration_ms)::float8
				FROM audit_log
				WHERE timestamp >= now() - $1::interval AND path = $2`,
				intervalArg(rc.Window), route).Scan(&p95)
			if err != nil {
				return 0, err
			}
			if p95 == nil {
				return 0, nil
			}
			return *p95, nil
		},
	})

	register(Resolver{
		Key: "http_route_error_rate_pct", Label: "Route error rate",
		Description: "5xx share for one route. Set labels to {\"route\":\"/api/payment/pay\"}.",
		Unit:        "percent", Suggested: 10, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			route := rc.Label("route", "")
			if route == "" {
				return 0, fmt.Errorf("this rule needs a route label, e.g. {\"route\":\"/api/payment/pay\"}")
			}
			var total, errs float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT count(*)::float8, count(*) FILTER (WHERE status_code >= 500)::float8
				FROM audit_log
				WHERE timestamp >= now() - $1::interval AND path = $2`,
				intervalArg(rc.Window), route).Scan(&total, &errs)
			if err != nil {
				return 0, err
			}
			return pct(errs, total), nil
		},
	})

	register(Resolver{
		Key: "http_requests_per_min", Label: "Request volume",
		Description: "Requests per minute. Useful with the 'lt' comparator to catch traffic disappearing.",
		Unit:        "per_min", Suggested: 1, Comparator: "lt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var n float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT count(*)::float8 FROM audit_log
				WHERE timestamp >= now() - $1::interval`, intervalArg(rc.Window)).Scan(&n)
			if err != nil {
				return 0, err
			}
			mins := rc.Window.Minutes()
			if mins <= 0 {
				return 0, nil
			}
			return n / mins, nil
		},
	})

	register(Resolver{
		Key: "auth_failure_rate_per_min", Label: "Failed logins per minute",
		Description: "Rejected authentication responses per minute. A spike means brute force or a broken login.",
		Unit:        "per_min", Suggested: 30, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var n float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT count(*)::float8 FROM audit_log
				WHERE timestamp >= now() - $1::interval
				  AND path LIKE '/api/auth/%'
				  AND status_code IN (401, 403, 429)`, intervalArg(rc.Window)).Scan(&n)
			if err != nil {
				return 0, err
			}
			mins := rc.Window.Minutes()
			if mins <= 0 {
				return 0, nil
			}
			return n / mins, nil
		},
	})

	// ── Database and Redis, from the process ────────────────────────────────

	register(Resolver{
		Key: "db_up", Label: "Database reachable",
		Description: "1 when the last Postgres health ping succeeded, 0 when it failed.",
		Unit:        "bool", Suggested: 1, Comparator: "lt", Source: "process",
		Fn: func(rc ResolveContext) (float64, error) {
			return imetrics.DBUp.With(rc.Label("database", "main")).Value(), nil
		},
	})

	register(Resolver{
		Key: "redis_up", Label: "Redis reachable",
		Description: "1 when the last Redis health ping succeeded, 0 when it failed.",
		Unit:        "bool", Suggested: 1, Comparator: "lt", Source: "process",
		Fn: func(rc ResolveContext) (float64, error) {
			return imetrics.RedisUp.With(rc.Label("instance", "main")).Value(), nil
		},
	})

	register(Resolver{
		Key: "db_query_p95_ms", Label: "Database query p95",
		Description: "95th percentile time spent inside a database query, measured in this process.",
		Unit:        "ms", Suggested: 500, Comparator: "gt", Source: "process",
		Fn: func(rc ResolveContext) (float64, error) {
			// The window stores seconds; alert thresholds are friendlier
			// in milliseconds.
			return imetrics.GetWindow(imetrics.WinDBLatency).Quantile(rc.Window, 0.95) * 1000, nil
		},
	})

	register(Resolver{
		Key: "db_query_p99_ms", Label: "Database query p99",
		Description: "99th percentile database query time — catches the slow tail the p95 hides.",
		Unit:        "ms", Suggested: 2000, Comparator: "gt", Source: "process",
		Fn: func(rc ResolveContext) (float64, error) {
			return imetrics.GetWindow(imetrics.WinDBLatency).Quantile(rc.Window, 0.99) * 1000, nil
		},
	})

	register(Resolver{
		Key: "db_errors_per_min", Label: "Database errors per minute",
		Description: "Failed queries per minute, excluding ordinary not-found results.",
		Unit:        "per_min", Suggested: 5, Comparator: "gt", Source: "process",
		Fn: func(rc ResolveContext) (float64, error) {
			return imetrics.GetWindow(imetrics.WinDBErrors).Rate(rc.Window), nil
		},
	})

	register(Resolver{
		Key: "db_pool_utilization", Label: "Connection pool utilization",
		Description: "Connections in use over the pool maximum, 0 to 1. Sustained high means every endpoint is queueing.",
		Unit:        "ratio", Suggested: 0.85, Comparator: "gt", Source: "process",
		Fn: func(rc ResolveContext) (float64, error) {
			if rc.Pool == nil {
				return 0, fmt.Errorf("no database pool available")
			}
			s := rc.Pool.Stat()
			if s.MaxConns() <= 0 {
				return 0, nil
			}
			return float64(s.AcquiredConns()) / float64(s.MaxConns()), nil
		},
	})

	// ── Extraction pipeline, from SQL ───────────────────────────────────────

	register(Resolver{
		Key: "queue_depth", Label: "Extraction queue depth",
		Description: "Jobs sitting in pending or processing right now.",
		Unit:        "count", Suggested: 100, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var n float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT count(*)::float8 FROM extraction_jobs
				WHERE status IN ('pending','processing')`).Scan(&n)
			return n, err
		},
	})

	register(Resolver{
		Key: "sla_breach_count", Label: "SLA breaches",
		Description: "Jobs past their SLA deadline that still have not completed.",
		Unit:        "count", Suggested: 5, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var n float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT count(*)::float8 FROM extraction_jobs
				WHERE status IN ('pending','processing') AND sla_deadline < now()`).Scan(&n)
			return n, err
		},
	})

	register(Resolver{
		Key: "oldest_pending_job_minutes", Label: "Oldest pending job age",
		Description: "How long the oldest unfinished job has been waiting, in minutes.",
		Unit:        "minutes", Suggested: 60, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var mins *float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT EXTRACT(EPOCH FROM (now() - min(created_at)))::float8 / 60
				FROM extraction_jobs WHERE status IN ('pending','processing')`).Scan(&mins)
			if err != nil {
				return 0, err
			}
			if mins == nil {
				return 0, nil
			}
			return *mins, nil
		},
	})

	register(Resolver{
		Key: "extraction_failure_rate_pct", Label: "Extraction failure rate",
		Description: "Share of finished extraction jobs that ended in failure.",
		Unit:        "percent", Suggested: 20, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var total, failed float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT count(*)::float8,
				       count(*) FILTER (WHERE status = 'failed')::float8
				FROM extraction_jobs
				WHERE created_at >= now() - $1::interval
				  AND status IN ('completed','failed')`,
				intervalArg(rc.Window)).Scan(&total, &failed)
			if err != nil {
				return 0, err
			}
			return pct(failed, total), nil
		},
	})

	register(Resolver{
		Key: "extraction_p95_seconds", Label: "Extraction p95 duration",
		Description: "95th percentile end-to-end extraction time for completed jobs.",
		Unit:        "seconds", Suggested: 300, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var p95 *float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT percentile_disc(0.95) WITHIN GROUP (
				         ORDER BY EXTRACT(EPOCH FROM (completed_at - created_at))
				       )::float8
				FROM extraction_jobs
				WHERE completed_at IS NOT NULL
				  AND created_at >= now() - $1::interval`,
				intervalArg(rc.Window)).Scan(&p95)
			if err != nil {
				return 0, err
			}
			if p95 == nil {
				return 0, nil
			}
			return *p95, nil
		},
	})

	register(Resolver{
		Key: "confidence_drift", Label: "Extraction confidence drift",
		Description: "Largest per-field drop in mean confidence versus the prior baseline week. 0.1 is a ten-point drop.",
		Unit:        "ratio", Suggested: 0.1, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var drift *float64
			// Mirrors GetDriftReport in internal/db/db.go: last 7 days
			// against the 7 before it, per field, keeping only fields with
			// enough samples to mean anything.
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT max(baseline_avg - recent_avg)::float8 FROM (
					SELECT field_name,
					       AVG(CASE WHEN corrected_at >= now() - interval '7 days'
					                THEN confidence_score END) AS recent_avg,
					       AVG(CASE WHEN corrected_at <  now() - interval '7 days'
					                 AND corrected_at >= now() - interval '14 days'
					                THEN confidence_score END) AS baseline_avg
					FROM corrections
					WHERE confidence_score IS NOT NULL
					GROUP BY field_name
					HAVING COUNT(*) >= 5
				) d WHERE recent_avg IS NOT NULL AND baseline_avg IS NOT NULL`).Scan(&drift)
			if err != nil {
				return 0, err
			}
			if drift == nil {
				return 0, nil
			}
			return *drift, nil
		},
	})

	// ── Money ───────────────────────────────────────────────────────────────

	register(Resolver{
		Key: "payment_failure_rate_pct", Label: "Payment failure rate",
		Description: "Share of payment transactions that failed. Catches a gateway outage that returns 200s.",
		Unit:        "percent", Suggested: 25, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var total, failed float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT count(*)::float8, count(*) FILTER (WHERE status = 'failed')::float8
				FROM plan_transactions WHERE created_at >= now() - $1::interval`,
				intervalArg(rc.Window)).Scan(&total, &failed)
			if err != nil {
				return 0, err
			}
			return pct(failed, total), nil
		},
	})

	register(Resolver{
		Key: "payment_stuck_pending", Label: "Payments stuck pending",
		Description: "Transactions left pending for over an hour — the charge started and nothing ever confirmed it.",
		Unit:        "count", Suggested: 3, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var n float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT count(*)::float8 FROM plan_transactions
				WHERE status = 'pending' AND created_at < now() - interval '1 hour'`).Scan(&n)
			return n, err
		},
	})

	// ── Coupons ─────────────────────────────────────────────────────────────

	register(Resolver{
		Key: "coupon_redemptions_per_min", Label: "Coupon redemptions per minute",
		Description: "Redemption rate. A sudden spike usually means a code leaked somewhere public.",
		Unit:        "per_min", Suggested: 20, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var n float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT count(*)::float8 FROM coupon_redemptions
				WHERE redeemed_at >= now() - $1::interval`, intervalArg(rc.Window)).Scan(&n)
			if err != nil {
				return 0, err
			}
			mins := rc.Window.Minutes()
			if mins <= 0 {
				return 0, nil
			}
			return n / mins, nil
		},
	})

	register(Resolver{
		Key: "coupon_discount_per_hour", Label: "Discount given per hour",
		Description: "Currency value discounted through coupons per hour.",
		Unit:        "count", Suggested: 1000, Comparator: "gt", Source: "sql",
		Fn: func(rc ResolveContext) (float64, error) {
			var total float64
			err := rc.Pool.QueryRow(rc.Ctx, `
				SELECT coalesce(sum(discount_amount),0)::float8 FROM coupon_redemptions
				WHERE redeemed_at >= now() - $1::interval`, intervalArg(rc.Window)).Scan(&total)
			if err != nil {
				return 0, err
			}
			hours := rc.Window.Hours()
			if hours <= 0 {
				return 0, nil
			}
			return total / hours, nil
		},
	})

	register(Resolver{
		Key: "coupon_rejection_rate_per_min", Label: "Rejected coupon attempts per minute",
		Description: "Failed redemption attempts per minute. Sustained non-zero means someone is guessing codes.",
		Unit:        "per_min", Suggested: 30, Comparator: "gt", Source: "process",
		Fn: func(rc ResolveContext) (float64, error) {
			return imetrics.GetWindow(imetrics.WinCouponRedeems).Rate(rc.Window), nil
		},
	})

	// ── Process health ──────────────────────────────────────────────────────

	register(Resolver{
		Key: "goroutines", Label: "Goroutine count",
		Description: "Live goroutines. A number that only ever rises is a leak.",
		Unit:        "count", Suggested: 10000, Comparator: "gt", Source: "process",
		Fn: func(rc ResolveContext) (float64, error) {
			return imetrics.GoroutineCount(), nil
		},
	})

	register(Resolver{
		Key: "heap_alloc_mb", Label: "Heap memory",
		Description: "Heap currently allocated, in megabytes.",
		Unit:        "count", Suggested: 2048, Comparator: "gt", Source: "process",
		Fn: func(rc ResolveContext) (float64, error) {
			return imetrics.HeapAllocBytes() / (1024 * 1024), nil
		},
	})

	register(Resolver{
		Key: "span_error_rate_pct", Label: "Component error rate",
		Description: "Error share for one instrumented component. Set labels to {\"component\":\"extractor\"}.",
		Unit:        "percent", Suggested: 10, Comparator: "gt", Source: "process",
		Fn: func(rc ResolveContext) (float64, error) {
			component := rc.Label("component", "")
			if component == "" {
				return 0, fmt.Errorf("this rule needs a component label, e.g. {\"component\":\"extractor\"}")
			}
			ok, failed := imetrics.SpanTotalsFor(component)
			return pct(failed, ok+failed), nil
		},
	})
}

// intervalArg renders a duration as a Postgres interval string. Passed as
// a parameter and cast with ::interval rather than interpolated, so no
// caller-controlled value ever reaches the SQL text.
func intervalArg(d time.Duration) string {
	if d <= 0 {
		d = 5 * time.Minute
	}
	return fmt.Sprintf("%d seconds", int(d.Seconds()))
}
