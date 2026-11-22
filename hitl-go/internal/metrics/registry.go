// internal/metrics/registry.go
//
// A small, dependency-free Prometheus-compatible metrics registry.
//
// WHY NOT prometheus/client_golang
//
// This package deliberately adds no new Go module dependency. The output
// format below is the Prometheus text exposition format (version 0.0.4),
// which is what /metrics has to produce and what Prometheus, Grafana
// Agent, VictoriaMetrics, OpenTelemetry Collector and every other scraper
// in that ecosystem parse. Nothing downstream can tell the difference —
// Grafana dashboards, PromQL and alerting rules all work identically.
//
// If you later decide you want client_golang anyway (for its process and
// Go runtime collectors, or exemplars), the swap is mechanical: the
// Counter/Gauge/Histogram method names here intentionally mirror its API
// (With, Inc, Add, Set, Observe), so call sites don't change.
//
// # CONCURRENCY
//
// Every type here is safe for concurrent use. Counters and gauges use
// atomics on the hot path. Histograms take a per-child mutex, held only
// long enough to bump a bucket and a sum — measured in nanoseconds, and
// never held across an I/O call.
package metrics

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// ── Registry ─────────────────────────────────────────────────────────────────

// Registry holds every metric that should appear on /metrics.
type Registry struct {
	mu         sync.RWMutex
	collectors []collector
}

type collector interface {
	collect(sb *strings.Builder)
}

// NewRegistry returns an empty registry. Most callers want the package
// level Default registry instead.
func NewRegistry() *Registry { return &Registry{} }

// Default is the registry every metric defined in metrics.go registers
// into, and the one Handler() exposes.
var Default = NewRegistry()

// Register adds a collector. Registering the same metric name twice is
// the caller's problem — Prometheus will complain about duplicate series
// at scrape time rather than here, because panicking at init over a
// metrics definition would take down a server that is otherwise fine.
func (r *Registry) Register(c collector) {
	r.mu.Lock()
	r.collectors = append(r.collectors, c)
	r.mu.Unlock()
}

// Gather renders every registered metric in Prometheus text format.
func (r *Registry) Gather() string {
	r.mu.RLock()
	cs := make([]collector, len(r.collectors))
	copy(cs, r.collectors)
	r.mu.RUnlock()

	var sb strings.Builder
	sb.Grow(16 * 1024)
	for _, c := range cs {
		c.collect(&sb)
	}
	return sb.String()
}

// ── Shared metadata ──────────────────────────────────────────────────────────

type desc struct {
	name       string
	help       string
	typ        string // "counter" | "gauge" | "histogram"
	labelNames []string
}

func (d desc) header(sb *strings.Builder) {
	sb.WriteString("# HELP ")
	sb.WriteString(d.name)
	sb.WriteByte(' ')
	sb.WriteString(escapeHelp(d.help))
	sb.WriteByte('\n')
	sb.WriteString("# TYPE ")
	sb.WriteString(d.name)
	sb.WriteByte(' ')
	sb.WriteString(d.typ)
	sb.WriteByte('\n')
}

// writeSeries emits one sample line: name{labels} value
func writeSeries(sb *strings.Builder, name string, labelNames, labelValues []string, extraName, extraValue string, value float64) {
	sb.WriteString(name)
	if len(labelNames) > 0 || extraName != "" {
		sb.WriteByte('{')
		first := true
		for i, ln := range labelNames {
			if !first {
				sb.WriteByte(',')
			}
			first = false
			sb.WriteString(ln)
			sb.WriteString(`="`)
			if i < len(labelValues) {
				sb.WriteString(escapeLabel(labelValues[i]))
			}
			sb.WriteByte('"')
		}
		if extraName != "" {
			if !first {
				sb.WriteByte(',')
			}
			sb.WriteString(extraName)
			sb.WriteString(`="`)
			sb.WriteString(extraValue)
			sb.WriteByte('"')
		}
		sb.WriteByte('}')
	}
	sb.WriteByte(' ')
	sb.WriteString(formatFloat(value))
	sb.WriteByte('\n')
}

func formatFloat(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case v == math.Trunc(v) && math.Abs(v) < 1e15:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
}

// escapeLabel escapes the three characters the text format reserves
// inside a label value. Route labels can contain none of these today,
// but error/reason labels built from real error strings certainly can.
func escapeLabel(s string) string {
	if !strings.ContainsAny(s, `\"`+"\n") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}

func escapeHelp(s string) string {
	if !strings.ContainsAny(s, `\`+"\n") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	return r.Replace(s)
}

// key joins label values into a map key. Uses \xff as the separator
// because it cannot appear in valid UTF-8, so two different label value
// sets can never collide into one key.
func key(values []string) string {
	return strings.Join(values, "\xff")
}

// ── Counter ──────────────────────────────────────────────────────────────────

// Counter is a single monotonically increasing series.
type Counter struct {
	bits   uint64 // float64 bits, atomic
	labels []string
}

// Inc adds 1.
func (c *Counter) Inc() { c.Add(1) }

// Add adds delta. Negative deltas are ignored — a counter that goes
// backwards breaks every rate() query built on top of it, so silently
// dropping the write is better than corrupting the series.
func (c *Counter) Add(delta float64) {
	if delta < 0 {
		return
	}
	for {
		old := atomic.LoadUint64(&c.bits)
		next := math.Float64bits(math.Float64frombits(old) + delta)
		if atomic.CompareAndSwapUint64(&c.bits, old, next) {
			return
		}
	}
}

// Value reads the current total.
func (c *Counter) Value() float64 {
	return math.Float64frombits(atomic.LoadUint64(&c.bits))
}

// CounterVec is a counter family partitioned by labels.
type CounterVec struct {
	desc
	mu       sync.RWMutex
	children map[string]*Counter
}

// NewCounter defines and registers a counter family on the Default registry.
func NewCounter(name, help string, labelNames ...string) *CounterVec {
	cv := &CounterVec{
		desc:     desc{name: name, help: help, typ: "counter", labelNames: labelNames},
		children: map[string]*Counter{},
	}
	Default.Register(cv)
	return cv
}

// With returns the child series for these label values, creating it on
// first use. The number of values must match the number of label names
// the family was defined with; extra values are ignored and missing ones
// render as empty strings rather than panicking, because a metrics call
// site should never be able to crash a request handler.
func (cv *CounterVec) With(values ...string) *Counter {
	k := key(values)
	cv.mu.RLock()
	c, ok := cv.children[k]
	cv.mu.RUnlock()
	if ok {
		return c
	}
	cv.mu.Lock()
	defer cv.mu.Unlock()
	if c, ok = cv.children[k]; ok {
		return c
	}
	c = &Counter{labels: append([]string(nil), values...)}
	cv.children[k] = c
	return c
}

func (cv *CounterVec) collect(sb *strings.Builder) {
	cv.mu.RLock()
	defer cv.mu.RUnlock()
	if len(cv.children) == 0 {
		return
	}
	cv.header(sb)
	for _, c := range sortedKeys(cv.children) {
		child := cv.children[c]
		writeSeries(sb, cv.name, cv.labelNames, child.labels, "", "", child.Value())
	}
}

// ── Gauge ────────────────────────────────────────────────────────────────────

// Gauge is a single value that can go up and down.
type Gauge struct {
	bits   uint64
	labels []string
}

func (g *Gauge) Set(v float64) { atomic.StoreUint64(&g.bits, math.Float64bits(v)) }
func (g *Gauge) Inc()          { g.Add(1) }
func (g *Gauge) Dec()          { g.Add(-1) }

func (g *Gauge) Add(delta float64) {
	for {
		old := atomic.LoadUint64(&g.bits)
		next := math.Float64bits(math.Float64frombits(old) + delta)
		if atomic.CompareAndSwapUint64(&g.bits, old, next) {
			return
		}
	}
}

func (g *Gauge) Value() float64 { return math.Float64frombits(atomic.LoadUint64(&g.bits)) }

// GaugeVec is a gauge family partitioned by labels.
type GaugeVec struct {
	desc
	mu       sync.RWMutex
	children map[string]*Gauge
}

func NewGauge(name, help string, labelNames ...string) *GaugeVec {
	gv := &GaugeVec{
		desc:     desc{name: name, help: help, typ: "gauge", labelNames: labelNames},
		children: map[string]*Gauge{},
	}
	Default.Register(gv)
	return gv
}

func (gv *GaugeVec) With(values ...string) *Gauge {
	k := key(values)
	gv.mu.RLock()
	g, ok := gv.children[k]
	gv.mu.RUnlock()
	if ok {
		return g
	}
	gv.mu.Lock()
	defer gv.mu.Unlock()
	if g, ok = gv.children[k]; ok {
		return g
	}
	g = &Gauge{labels: append([]string(nil), values...)}
	gv.children[k] = g
	return g
}

func (gv *GaugeVec) collect(sb *strings.Builder) {
	gv.mu.RLock()
	defer gv.mu.RUnlock()
	if len(gv.children) == 0 {
		return
	}
	gv.header(sb)
	for _, k := range sortedKeys(gv.children) {
		child := gv.children[k]
		writeSeries(sb, gv.name, gv.labelNames, child.labels, "", "", child.Value())
	}
}

// ── GaugeFunc ────────────────────────────────────────────────────────────────

// GaugeFunc is a gauge whose value is computed at scrape time rather than
// written by the application. Use it for things you can already read
// cheaply from somewhere else — pool.Stat(), runtime.MemStats, a queue
// length — so nothing has to remember to keep a gauge in sync.
//
// The function runs inside the scrape request. Keep it fast and make sure
// it cannot block: a GaugeFunc that does a database round trip will hang
// /metrics when the database is down, which is exactly when you most need
// the scrape to succeed.
type GaugeFunc struct {
	desc
	fn func() float64
}

func NewGaugeFunc(name, help string, fn func() float64) *GaugeFunc {
	gf := &GaugeFunc{desc: desc{name: name, help: help, typ: "gauge"}, fn: fn}
	Default.Register(gf)
	return gf
}

func (gf *GaugeFunc) collect(sb *strings.Builder) {
	gf.header(sb)
	writeSeries(sb, gf.name, nil, nil, "", "", gf.fn())
}

// ── Histogram ────────────────────────────────────────────────────────────────

// DefaultBuckets covers sub-millisecond to ~30s in seconds. Sized for
// HTTP and database work in this system: dense where normal traffic sits
// (5ms–1s) and sparse out in the tail where you only care that something
// was slow, not precisely how slow.
var DefaultBuckets = []float64{
	0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30,
}

// LLMBuckets is for calls that are expected to take seconds, not
// milliseconds — the LLM round trips and full-document extraction. Using
// DefaultBuckets for these would put almost every observation in the last
// two buckets and make the quantiles useless.
var LLMBuckets = []float64{
	0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300,
}

// Histogram is one histogram series: cumulative bucket counts, a sum, and
// a total count.
type Histogram struct {
	mu      sync.Mutex
	buckets []float64 // upper bounds, ascending, shared with the family
	counts  []uint64  // per-bucket (non-cumulative); summed at collect time
	sum     float64
	count   uint64
	labels  []string
}

// Observe records one measurement, in the family's base unit (seconds for
// every duration histogram in this codebase — Prometheus convention is
// base units, and Grafana formats them for display).
func (h *Histogram) Observe(v float64) {
	// sort.SearchFloat64s finds the first bucket whose upper bound is >= v,
	// which is the definition of the bucket v belongs in. Values above the
	// largest bound land at index len(buckets), counted only in +Inf.
	i := sort.SearchFloat64s(h.buckets, v)
	h.mu.Lock()
	if i < len(h.counts) {
		h.counts[i]++
	}
	h.sum += v
	h.count++
	h.mu.Unlock()
}

// Count returns how many observations this series has taken.
func (h *Histogram) Count() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

// Sum returns the total of every observed value.
func (h *Histogram) Sum() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sum
}

// HistogramVec is a histogram family partitioned by labels.
type HistogramVec struct {
	desc
	buckets  []float64
	mu       sync.RWMutex
	children map[string]*Histogram
}

// NewHistogram defines and registers a histogram family. Pass nil buckets
// to use DefaultBuckets.
func NewHistogram(name, help string, buckets []float64, labelNames ...string) *HistogramVec {
	if len(buckets) == 0 {
		buckets = DefaultBuckets
	}
	b := append([]float64(nil), buckets...)
	sort.Float64s(b)
	hv := &HistogramVec{
		desc:     desc{name: name, help: help, typ: "histogram", labelNames: labelNames},
		buckets:  b,
		children: map[string]*Histogram{},
	}
	Default.Register(hv)
	return hv
}

func (hv *HistogramVec) With(values ...string) *Histogram {
	k := key(values)
	hv.mu.RLock()
	h, ok := hv.children[k]
	hv.mu.RUnlock()
	if ok {
		return h
	}
	hv.mu.Lock()
	defer hv.mu.Unlock()
	if h, ok = hv.children[k]; ok {
		return h
	}
	h = &Histogram{
		buckets: hv.buckets,
		// One slot per bucket plus one for +Inf.
		counts: make([]uint64, len(hv.buckets)+1),
		labels: append([]string(nil), values...),
	}
	hv.children[k] = h
	return h
}

func (hv *HistogramVec) collect(sb *strings.Builder) {
	hv.mu.RLock()
	defer hv.mu.RUnlock()
	if len(hv.children) == 0 {
		return
	}
	hv.header(sb)
	for _, k := range sortedKeys(hv.children) {
		child := hv.children[k]
		child.mu.Lock()
		cumulative := uint64(0)
		for i, ub := range hv.buckets {
			cumulative += child.counts[i]
			writeSeries(sb, hv.name+"_bucket", hv.labelNames, child.labels,
				"le", formatFloat(ub), float64(cumulative))
		}
		// The +Inf bucket must equal _count, per the exposition format.
		writeSeries(sb, hv.name+"_bucket", hv.labelNames, child.labels,
			"le", "+Inf", float64(child.count))
		sum, count := child.sum, child.count
		child.mu.Unlock()

		writeSeries(sb, hv.name+"_sum", hv.labelNames, child.labels, "", "", sum)
		writeSeries(sb, hv.name+"_count", hv.labelNames, child.labels, "", "", float64(count))
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// sortedKeys gives collect() a stable output order. Prometheus does not
// require sorted output, but stable ordering makes /metrics diffable by
// hand, which matters a lot when you are debugging why a series is
// missing.
func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
