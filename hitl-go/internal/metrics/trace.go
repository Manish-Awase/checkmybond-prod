// internal/metrics/trace.go
//
// Tracing and structured logging, wired to the same span that produces
// the metrics — so a latency spike on a graph and the log lines that
// explain it share an identifier you can search on.
//
// # THE MODEL
//
// A trace id is created at the edge (the gateway, or the server if a
// request arrives without one) and travels with the request: in the
// context for in-process work, and in the X-Trace-Id header when one
// service calls another or hands work to Celery. Every span logs it.
// Every alert email carries the trace id of the evaluation that fired it.
//
// This is not OpenTelemetry and does not pretend to be. There is no
// collector, no sampling, no span export — spans become a duration
// histogram plus a log line. That covers the question this system
// actually asks ("which part is slow, and what happened during the slow
// one") without adding an OTel collector to the deployment. If you later
// want real distributed traces in Tempo or Jaeger, the Span API below is
// the one place to change: the call sites stay as they are.
//
// USAGE
//
//	// The common case — wrap any unit of work:
//	err := metrics.Span(ctx, "extractor", "parse_pdf", func(ctx context.Context) error {
//	        return parse(ctx, doc)
//	})
//
//	// When you need the handle (to attach fields discovered mid-work):
//	sp := metrics.StartSpan(ctx, "llm", "complete")
//	sp.Set("model", model)
//	resp, err := call(sp.Context())
//	sp.End(err)
package metrics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// TraceHeader is the wire header trace ids travel in. Deliberately the
// same name on every hop — gateway, server, and the Celery payload field
// in internal/queue — so a grep for one id finds every leg.
const TraceHeader = "X-Trace-Id"

type ctxKey string

const (
	ctxTraceID ctxKey = "bondqi_trace_id"
	ctxSpanID  ctxKey = "bondqi_span_id"
)

// ── Trace ids ────────────────────────────────────────────────────────────────

// NewTraceID returns a random 128-bit id, hex encoded — the same shape
// OpenTelemetry uses, so these ids stay meaningful if you migrate later.
func NewTraceID() string { return randomHex(16) }

// NewSpanID returns a random 64-bit id, hex encoded.
func NewSpanID() string { return randomHex(8) }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is close to unrecoverable, but a metrics
		// helper must never take down a request. Fall back to something
		// time-derived: correlation degrades, nothing breaks.
		return fmt.Sprintf("%0*x", n*2, time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// WithTraceID puts a trace id on the context, generating one if empty.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if traceID == "" {
		traceID = NewTraceID()
	}
	return context.WithValue(ctx, ctxTraceID, traceID)
}

// TraceID reads the trace id, or "" if the context never passed through
// an instrumented edge.
func TraceID(ctx context.Context) string {
	v, _ := ctx.Value(ctxTraceID).(string)
	return v
}

// SpanID reads the current innermost span id.
func SpanID(ctx context.Context) string {
	v, _ := ctx.Value(ctxSpanID).(string)
	return v
}

// ── Spans ────────────────────────────────────────────────────────────────────

// SpanHandle is one in-progress measurement.
type SpanHandle struct {
	ctx       context.Context
	component string
	operation string
	traceID   string
	spanID    string
	parentID  string
	start     time.Time

	mu     sync.Mutex
	fields map[string]any
	ended  bool
}

// StartSpan begins a span. Always pair it with End, normally deferred.
func StartSpan(ctx context.Context, component, operation string) *SpanHandle {
	if ctx == nil {
		ctx = context.Background()
	}
	traceID := TraceID(ctx)
	if traceID == "" {
		traceID = NewTraceID()
		ctx = context.WithValue(ctx, ctxTraceID, traceID)
	}
	parent := SpanID(ctx)
	spanID := NewSpanID()
	ctx = context.WithValue(ctx, ctxSpanID, spanID)

	SpanInFlight.With(component, operation).Inc()

	return &SpanHandle{
		ctx:       ctx,
		component: component,
		operation: operation,
		traceID:   traceID,
		spanID:    spanID,
		parentID:  parent,
		start:     time.Now(),
		fields:    map[string]any{},
	}
}

// Context returns the span's context — pass this to callees so nested
// spans nest properly.
func (s *SpanHandle) Context() context.Context { return s.ctx }

// TraceID exposes the id for putting in a response header or a job payload.
func (s *SpanHandle) TraceID() string { return s.traceID }

// Set attaches a field to the span's log line. Keep values low-detail and
// non-sensitive: these end up in logs. Field values never become metric
// labels, so it is safe to put a document id here — it would not be safe
// on a metric.
func (s *SpanHandle) Set(k string, v any) *SpanHandle {
	s.mu.Lock()
	s.fields[k] = v
	s.mu.Unlock()
	return s
}

// End closes the span, records the duration histogram and counters, and
// emits one structured log line. Passing a non-nil err marks the span
// failed and classifies the error for the metric label.
//
// Calling End twice is a no-op the second time — that makes
// `defer sp.End(nil)` alongside an explicit `sp.End(err)` on the error
// path safe, which is a pattern that otherwise double-counts.
func (s *SpanHandle) End(err error) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	fields := make(map[string]any, len(s.fields))
	for k, v := range s.fields {
		fields[k] = v
	}
	s.mu.Unlock()

	elapsed := time.Since(s.start)
	status := "ok"
	if err != nil {
		status = "error"
	}

	SpanInFlight.With(s.component, s.operation).Dec()
	SpanDuration.With(s.component, s.operation, status).Observe(elapsed.Seconds())
	SpanTotal.With(s.component, s.operation, status).Inc()

	level := "info"
	if err != nil {
		kind := ClassifyError(err)
		SpanErrors.With(s.component, s.operation, kind).Inc()
		fields["error"] = err.Error()
		fields["error_kind"] = kind
		level = "error"
	}

	fields["component"] = s.component
	fields["operation"] = s.operation
	fields["duration_ms"] = round2(float64(elapsed.Microseconds()) / 1000)
	fields["status"] = status
	fields["trace_id"] = s.traceID
	fields["span_id"] = s.spanID
	if s.parentID != "" {
		fields["parent_span_id"] = s.parentID
	}

	Log(level, s.component+"."+s.operation, fields)
}

// Span is the convenience form: start, run, end, propagate the error.
func Span(ctx context.Context, component, operation string, fn func(context.Context) error) error {
	sp := StartSpan(ctx, component, operation)
	err := fn(sp.Context())
	sp.End(err)
	return err
}

// SpanValue is Span for work that returns a value as well as an error.
// Generic so the value keeps its type instead of going through `any`.
func SpanValue[T any](ctx context.Context, component, operation string, fn func(context.Context) (T, error)) (T, error) {
	sp := StartSpan(ctx, component, operation)
	v, err := fn(sp.Context())
	sp.End(err)
	return v, err
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// ── Error classification ─────────────────────────────────────────────────────

// ClassifyError maps an error onto a small, bounded set of kinds suitable
// for a metric label.
//
// This function exists specifically to keep err.Error() out of labels.
// Error strings contain ids, hostnames, SQL fragments and user input, and
// putting them on a metric creates unbounded cardinality — one new series
// per distinct error message. The classification below is coarse on
// purpose: the metric tells you a class of failure is happening and how
// fast, and the structured log (which does carry the full message, keyed
// by the same trace id) tells you what it actually said.
func ClassifyError(err error) string {
	if err == nil {
		return "none"
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "network"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "network"
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "no rows"):
		return "not_found"
	case strings.Contains(msg, "duplicate key"), strings.Contains(msg, "unique constraint"):
		return "conflict"
	case strings.Contains(msg, "foreign key"):
		return "constraint"
	case strings.Contains(msg, "deadlock"):
		return "deadlock"
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "connection reset"):
		return "unavailable"
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"), strings.Contains(msg, "deadline"):
		return "timeout"
	case strings.Contains(msg, "permission"), strings.Contains(msg, "forbidden"), strings.Contains(msg, "unauthor"):
		return "permission"
	case strings.Contains(msg, "rate limit"), strings.Contains(msg, "too many requests"), strings.Contains(msg, "429"):
		return "rate_limited"
	case strings.Contains(msg, "invalid"), strings.Contains(msg, "malformed"), strings.Contains(msg, "parse"):
		return "invalid_input"
	default:
		return "internal"
	}
}

// ── Structured logging ───────────────────────────────────────────────────────

// LogFormat picks the output shape. LOG_FORMAT=json (the default in
// production) emits one JSON object per line, which is what Loki,
// CloudWatch, Datadog and every other log pipeline wants. LOG_FORMAT=text
// emits a readable line for local development, where JSON in a terminal
// is a downgrade.
func logFormat() string { return envOr("LOG_FORMAT", "json") }

var logMu sync.Mutex

// Log writes one structured line. Callers normally reach this through a
// span rather than directly, but it is exported for the places that have
// something to say without a duration attached.
func Log(level, message string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["level"] = level
	fields["msg"] = message
	fields["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	if svc := os.Getenv("SERVICE_NAME"); svc != "" {
		fields["service"] = svc
	}

	logMu.Lock()
	defer logMu.Unlock()

	if logFormat() == "text" {
		var sb strings.Builder
		sb.WriteString(strings.ToUpper(level))
		sb.WriteByte(' ')
		sb.WriteString(message)
		for _, k := range sortedKeys(fields) {
			if k == "level" || k == "msg" || k == "ts" {
				continue
			}
			fmt.Fprintf(&sb, " %s=%v", k, fields[k])
		}
		fmt.Fprintln(os.Stdout, sb.String())
		return
	}

	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(fields); err != nil {
		// Some field was not JSON-serializable. Losing the line entirely
		// would be worse than losing the fields.
		fmt.Fprintf(os.Stdout, `{"level":%q,"msg":%q,"log_error":%q}`+"\n", level, message, err.Error())
	}
}

// LogCtx is Log with the trace and span ids filled in from the context —
// the form to use inside a handler so a plain log line joins up with the
// spans around it.
func LogCtx(ctx context.Context, level, message string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	if t := TraceID(ctx); t != "" {
		fields["trace_id"] = t
	}
	if s := SpanID(ctx); s != "" {
		fields["span_id"] = s
	}
	Log(level, message, fields)
}
