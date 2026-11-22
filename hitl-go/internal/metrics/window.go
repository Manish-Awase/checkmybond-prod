// internal/metrics/window.go
//
// Rolling time windows, used by the alert engine.
//
// # WHY THIS EXISTS ALONGSIDE THE HISTOGRAMS
//
// A Prometheus histogram is cumulative since process start. That is
// exactly right for a scraper, which computes rates and quantiles by
// differencing two scrapes — but it is useless to code inside this
// process that needs to answer "what is the p95 over the last 5 minutes,
// right now". Reading a cumulative histogram would give you the p95 since
// the server booted, so one bad afternoon last week would keep an alert
// firing forever.
//
// So durations get recorded twice: into a histogram for Prometheus, and
// into a rolling window here for internal alerting. The double write is
// two atomic-ish operations on the same hot path, which is cheap, and it
// means the alert engine works with no Prometheus deployed at all —
// alert emails do not depend on the monitoring stack being up, which is a
// property worth paying a little for, since "Prometheus is down" and
// "everything is down" tend to happen together.
//
// # MEMORY
//
// Each window is a fixed-capacity ring buffer, so memory is bounded and
// allocated once. At the default 8192 samples a window costs ~128 KB. If
// a window overflows inside its retention period the oldest samples are
// overwritten, which biases quantiles toward recent traffic — acceptable,
// and the alternative (unbounded growth under load) is much worse.
package metrics

import (
	"math"
	"sort"
	"sync"
	"time"
)

const defaultWindowCapacity = 8192

type sample struct {
	at time.Time
	v  float64
}

// Window is a fixed-capacity ring buffer of timestamped observations.
type Window struct {
	mu   sync.Mutex
	buf  []sample
	next int
	full bool
}

// NewWindow creates a window holding up to capacity samples. Pass 0 for
// the default.
func NewWindow(capacity int) *Window {
	if capacity <= 0 {
		capacity = defaultWindowCapacity
	}
	return &Window{buf: make([]sample, capacity)}
}

// Observe records a value at the current time.
func (w *Window) Observe(v float64) { w.ObserveAt(time.Now(), v) }

// ObserveAt records a value at an explicit time. Exported mainly so tests
// can build a window without sleeping.
func (w *Window) ObserveAt(at time.Time, v float64) {
	w.mu.Lock()
	w.buf[w.next] = sample{at: at, v: v}
	w.next++
	if w.next == len(w.buf) {
		w.next = 0
		w.full = true
	}
	w.mu.Unlock()
}

// snapshot copies every sample newer than cutoff.
func (w *Window) snapshot(cutoff time.Time) []float64 {
	w.mu.Lock()
	defer w.mu.Unlock()

	n := w.next
	if w.full {
		n = len(w.buf)
	}
	out := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		s := w.buf[i]
		if s.at.IsZero() || s.at.Before(cutoff) {
			continue
		}
		out = append(out, s.v)
	}
	return out
}

// Count returns how many observations fall inside d.
func (w *Window) Count(d time.Duration) int {
	return len(w.snapshot(time.Now().Add(-d)))
}

// Sum totals every observation inside d.
func (w *Window) Sum(d time.Duration) float64 {
	total := 0.0
	for _, v := range w.snapshot(time.Now().Add(-d)) {
		total += v
	}
	return total
}

// Mean averages observations inside d. Returns 0 for an empty window
// rather than NaN — an alert comparing NaN to a threshold is always
// false, which silently disables the rule, whereas 0 at least compares
// predictably.
func (w *Window) Mean(d time.Duration) float64 {
	vals := w.snapshot(time.Now().Add(-d))
	if len(vals) == 0 {
		return 0
	}
	total := 0.0
	for _, v := range vals {
		total += v
	}
	return total / float64(len(vals))
}

// Quantile returns the q-th quantile (0..1) of observations inside d,
// using nearest-rank on the sorted samples. Unlike a bucket-interpolated
// histogram quantile this is exact for the samples retained, which is why
// alert thresholds are worth evaluating here rather than off the
// histogram.
func (w *Window) Quantile(d time.Duration, q float64) float64 {
	vals := w.snapshot(time.Now().Add(-d))
	if len(vals) == 0 {
		return 0
	}
	sort.Float64s(vals)
	if q <= 0 {
		return vals[0]
	}
	if q >= 1 {
		return vals[len(vals)-1]
	}
	idx := int(math.Ceil(q*float64(len(vals)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(vals) {
		idx = len(vals) - 1
	}
	return vals[idx]
}

// Rate returns observations per minute over d.
func (w *Window) Rate(d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(w.Count(d)) / d.Minutes()
}

// ── Named window registry ────────────────────────────────────────────────────

var (
	windowsMu sync.RWMutex
	windows   = map[string]*Window{}
)

// GetWindow returns the named window, creating it on first use. Names are
// plain strings (e.g. "http.latency", "db.latency", "http.5xx") and are
// referenced by the alert resolvers in internal/alerting.
func GetWindow(name string) *Window {
	windowsMu.RLock()
	w, ok := windows[name]
	windowsMu.RUnlock()
	if ok {
		return w
	}
	windowsMu.Lock()
	defer windowsMu.Unlock()
	if w, ok = windows[name]; ok {
		return w
	}
	w = NewWindow(defaultWindowCapacity)
	windows[name] = w
	return w
}

// Windows used across the codebase. Declared here so a typo in a resolver
// creates a compile error instead of silently reading an empty window that
// makes an alert never fire.
const (
	WinHTTPLatency   = "http.latency"     // seconds per request
	WinHTTPRequests  = "http.requests"    // 1 per request
	WinHTTP5xx       = "http.5xx"         // 1 per 5xx response
	WinHTTP4xx       = "http.4xx"         // 1 per 4xx response
	WinDBLatency     = "db.latency"       // seconds per query
	WinDBErrors      = "db.errors"        // 1 per failed query
	WinAuthFailures  = "auth.failures"    // 1 per rejected login
	WinCouponRedeems = "coupon.redeem"    // 1 per redemption
	WinPaymentFails  = "payment.failures" // 1 per failed charge
	WinExtractFails  = "extract.failures" // 1 per failed extraction
	WinExtractDone   = "extract.done"     // 1 per completed extraction
)
