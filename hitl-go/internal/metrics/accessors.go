// internal/metrics/accessors.go
//
// Read access to live metric values, for code inside this process that
// needs to act on them rather than export them — principally the alert
// engine in internal/alerting, which evaluates rules without a round trip
// through Prometheus.
//
// That independence is the point. If alerting queried Prometheus, then
// "Prometheus is unreachable" would mean "no alerts", and those two
// failures correlate: the same network partition or node failure takes
// out both. Reading in-process means the email about the database being
// down still goes out when the whole monitoring stack is unreachable.
package metrics

import "runtime"

// GoroutineCount returns live goroutines.
func GoroutineCount() float64 { return float64(runtime.NumGoroutine()) }

// HeapAllocBytes returns currently allocated heap bytes.
func HeapAllocBytes() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc)
}

// Walk visits every child series of a counter family. The labels slice
// passed to fn is the child's own backing array — treat it as read-only.
func (cv *CounterVec) Walk(fn func(labels []string, value float64)) {
	cv.mu.RLock()
	defer cv.mu.RUnlock()
	for _, c := range cv.children {
		fn(c.labels, c.Value())
	}
}

// Walk visits every child series of a gauge family.
func (gv *GaugeVec) Walk(fn func(labels []string, value float64)) {
	gv.mu.RLock()
	defer gv.mu.RUnlock()
	for _, g := range gv.children {
		fn(g.labels, g.Value())
	}
}

// LabelNames exposes the family's label names, so a caller can find the
// index of the label it wants instead of hardcoding a position.
func (cv *CounterVec) LabelNames() []string { return cv.labelNames }
func (gv *GaugeVec) LabelNames() []string   { return gv.labelNames }

// SpanTotalsFor sums span outcomes for one component across every
// operation, returning (ok, failed).
//
// SpanTotal is defined as (component, operation, status), so this walks
// the family and matches on the first label. It is O(number of series),
// which is fine: the alert engine calls it once per evaluation tick, not
// per request.
func SpanTotalsFor(component string) (ok, failed float64) {
	SpanTotal.Walk(func(labels []string, v float64) {
		if len(labels) < 3 || labels[0] != component {
			return
		}
		if labels[2] == "ok" {
			ok += v
		} else {
			failed += v
		}
	})
	return ok, failed
}

// HTTPTotalsFor sums request outcomes for one service across every route,
// returning (total, serverErrors).
func HTTPTotalsFor(service string) (total, serverErrors float64) {
	HTTPRequests.Walk(func(labels []string, v float64) {
		// labels are (service, method, route, status)
		if len(labels) < 4 || labels[0] != service {
			return
		}
		total += v
		if len(labels[3]) == 3 && labels[3][0] == '5' {
			serverErrors += v
		}
	})
	return total, serverErrors
}
