// internal/metrics/db.go
//
// Database and Redis instrumentation.
//
// Kept dependency-free on purpose: nothing here imports pgx or go-redis,
// it just wraps a func. That means this package compiles and tests on its
// own, and the same helpers work for Postgres, Redis, Qdrant or an HTTP
// call to a third party without needing a driver-specific integration for
// each.
//
// # WIRING IT IN
//
// The lowest-effort integration is at the query sites in
// internal/db/db.go:
//
//	func GetUserByID(ctx context.Context, id string) (*User, error) {
//	        return metrics.TrackDBValue(ctx, "select", "users", func(ctx context.Context) (*User, error) {
//	                row := pool.QueryRow(ctx, `SELECT ... WHERE id = $1`, id)
//	                return scanUser(row)
//	        })
//	}
//
// See ops/INTEGRATION.md for the full list of suggested call sites. You do
// not have to convert every query at once — instrument the ones on the
// critical path first (auth, upload, queue, payment) and the latency
// alerts become useful immediately.
package metrics

import (
	"context"
	"time"
)

// TrackDB measures a database operation that returns only an error.
//
// `op` is the logical operation (select, insert, update, delete, tx) and
// `table` is the primary table involved. Both become metric labels, so
// both must come from the call site as constants — never from a variable
// built out of user input.
func TrackDB(ctx context.Context, op, table string, fn func(context.Context) error) error {
	start := time.Now()
	err := fn(ctx)
	recordDB(ctx, op, table, start, err)
	return err
}

// TrackDBValue is TrackDB for a query that returns a value.
func TrackDBValue[T any](ctx context.Context, op, table string, fn func(context.Context) (T, error)) (T, error) {
	start := time.Now()
	v, err := fn(ctx)
	recordDB(ctx, op, table, start, err)
	return v, err
}

func recordDB(ctx context.Context, op, table string, start time.Time, err error) {
	elapsed := time.Since(start)
	seconds := elapsed.Seconds()

	DBDuration.With(op, table).Observe(seconds)
	GetWindow(WinDBLatency).Observe(seconds)

	if err != nil {
		kind := ClassifyError(err)
		DBQueries.With(op, table, "error").Inc()
		DBErrors.With(op, table, kind).Inc()

		// A row simply not existing is a normal result for a lookup, not a
		// database fault. Counting it toward the error-rate alert would
		// make "user typed an unknown coupon code" look like an outage.
		if kind != "not_found" {
			GetWindow(WinDBErrors).Observe(1)
		}

		LogCtx(ctx, "error", "db_query_failed", map[string]any{
			"op":          op,
			"table":       table,
			"duration_ms": round2(float64(elapsed.Microseconds()) / 1000),
			"error":       err.Error(),
			"error_kind":  kind,
		})
		return
	}

	DBQueries.With(op, table, "ok").Inc()

	// Log only the slow ones. Logging every query would bury everything
	// else and cost more than the queries do.
	if elapsed >= slowQueryThreshold() {
		LogCtx(ctx, "warn", "db_query_slow", map[string]any{
			"op":          op,
			"table":       table,
			"duration_ms": round2(float64(elapsed.Microseconds()) / 1000),
		})
	}
}

func slowQueryThreshold() time.Duration {
	if v := envOr("SLOW_QUERY_MS", ""); v != "" {
		if ms, err := time.ParseDuration(v + "ms"); err == nil {
			return ms
		}
	}
	return 500 * time.Millisecond
}

// ── Health pings ─────────────────────────────────────────────────────────────

// RecordDBPing updates bondqi_db_up and the ping latency histogram.
// Call this from a background ticker, not from a request handler — see
// StartHealthProbe below.
func RecordDBPing(database string, d time.Duration, err error) {
	if err != nil {
		DBUp.With(database).Set(0)
		return
	}
	DBUp.With(database).Set(1)
	DBPingDuration.With(database).Observe(d.Seconds())
}

// RecordRedisPing updates bondqi_redis_up.
func RecordRedisPing(instance string, d time.Duration, err error) {
	if err != nil {
		RedisUp.With(instance).Set(0)
		return
	}
	RedisUp.With(instance).Set(1)
	RedisDuration.With("ping").Observe(d.Seconds())
}

// TrackRedis measures one Redis command.
func TrackRedis(ctx context.Context, command string, fn func(context.Context) error) error {
	start := time.Now()
	err := fn(ctx)
	d := time.Since(start)
	RedisDuration.With(command).Observe(d.Seconds())
	if err != nil {
		RedisCommands.With(command, "error").Inc()
		return err
	}
	RedisCommands.With(command, "ok").Inc()
	return nil
}

// ── Background health probe ──────────────────────────────────────────────────

// HealthProbe is a named liveness check. Return nil for healthy.
type HealthProbe struct {
	Name  string
	Check func(context.Context) error
	// Kind selects which gauge gets updated: "db" or "redis". Anything
	// else is recorded as a generic span instead, which still gives you
	// bondqi_spans_total{component="health"} to alert on.
	Kind string
}

// StartHealthProbes runs every probe on an interval until ctx is done.
//
// Health is sampled on a ticker rather than computed at scrape time
// deliberately. A GaugeFunc that pings Postgres would block the whole
// /metrics response for the connect timeout whenever the database was
// down — so the scrape would fail exactly when you need the
// bondqi_db_up=0 sample to reach Prometheus and fire the alert. Sampling
// in the background keeps the scrape instant and always answerable.
func StartHealthProbes(ctx context.Context, interval time.Duration, probes ...HealthProbe) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	go func() {
		// Probe once immediately so the gauges are populated before the
		// first scrape rather than reading 0 (= "down") for one interval.
		runProbes(ctx, probes)

		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runProbes(ctx, probes)
			}
		}
	}()
}

func runProbes(ctx context.Context, probes []HealthProbe) {
	for _, p := range probes {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		start := time.Now()
		err := p.Check(probeCtx)
		d := time.Since(start)
		cancel()

		switch p.Kind {
		case "db":
			RecordDBPing(p.Name, d, err)
		case "redis":
			RecordRedisPing(p.Name, d, err)
		default:
			status := "ok"
			if err != nil {
				status = "error"
			}
			SpanDuration.With("health", p.Name, status).Observe(d.Seconds())
			SpanTotal.With("health", p.Name, status).Inc()
		}

		if err != nil {
			Log("error", "health_probe_failed", map[string]any{
				"probe":       p.Name,
				"kind":        p.Kind,
				"error":       err.Error(),
				"duration_ms": round2(float64(d.Microseconds()) / 1000),
			})
		}
	}
}

// ── Connection pool ──────────────────────────────────────────────────────────

// PoolStats is the subset of pgxpool.Stat worth graphing. Defined here
// rather than importing pgx so this package stays driver-free; the caller
// converts.
type PoolStats struct {
	Acquired        int32
	Idle            int32
	Total           int32
	Max             int32
	AcquireCount    int64
	AcquireDuration time.Duration
	EmptyAcquires   int64
}

// RegisterPoolCollector exposes connection pool gauges, read at scrape
// time from the supplied function.
//
// Saturation here is the single best early warning this system has. A
// pool sitting at Max with EmptyAcquires climbing means requests are
// queueing for a connection, and every endpoint gets slower at once —
// which looks like "the whole app is slow" long before any single query
// looks slow.
//
// Call once at startup:
//
//	metrics.RegisterPoolCollector("main", func() metrics.PoolStats {
//	        s := idb.Pool().Stat()
//	        return metrics.PoolStats{
//	                Acquired:      s.AcquiredConns(),
//	                Idle:          s.IdleConns(),
//	                Total:         s.TotalConns(),
//	                Max:           s.MaxConns(),
//	                AcquireCount:  s.AcquireCount(),
//	                EmptyAcquires: s.EmptyAcquireCount(),
//	        }
//	})
func RegisterPoolCollector(name string, fn func() PoolStats) {
	NewGaugeFunc("bondqi_db_pool_acquired_conns",
		"Connections currently checked out of the pool.",
		func() float64 { return float64(fn().Acquired) })

	NewGaugeFunc("bondqi_db_pool_idle_conns",
		"Idle connections available in the pool.",
		func() float64 { return float64(fn().Idle) })

	NewGaugeFunc("bondqi_db_pool_total_conns",
		"Connections currently open.",
		func() float64 { return float64(fn().Total) })

	NewGaugeFunc("bondqi_db_pool_max_conns",
		"Configured pool ceiling.",
		func() float64 { return float64(fn().Max) })

	NewGaugeFunc("bondqi_db_pool_empty_acquires_total",
		"Cumulative acquires that had to wait because the pool was empty. Rising means pool saturation.",
		func() float64 { return float64(fn().EmptyAcquires) })

	NewGaugeFunc("bondqi_db_pool_utilization_ratio",
		"Acquired over max, 0 to 1. Sustained above 0.8 means the pool needs raising or the queries need shortening.",
		func() float64 {
			s := fn()
			if s.Max <= 0 {
				return 0
			}
			return float64(s.Acquired) / float64(s.Max)
		})
}
