package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCounterExposition(t *testing.T) {
	r := NewRegistry()
	cv := &CounterVec{
		desc:     desc{name: "test_total", help: "help text", typ: "counter", labelNames: []string{"a"}},
		children: map[string]*Counter{},
	}
	r.Register(cv)
	cv.With("x").Inc()
	cv.With("x").Add(4)
	cv.With("y").Inc()

	out := r.Gather()
	if !strings.Contains(out, "# TYPE test_total counter") {
		t.Fatalf("missing TYPE line:\n%s", out)
	}
	if !strings.Contains(out, `test_total{a="x"} 5`) {
		t.Fatalf("wrong value for x:\n%s", out)
	}
	if !strings.Contains(out, `test_total{a="y"} 1`) {
		t.Fatalf("wrong value for y:\n%s", out)
	}
}

func TestCounterRejectsNegative(t *testing.T) {
	c := &Counter{}
	c.Add(5)
	c.Add(-3)
	if got := c.Value(); got != 5 {
		t.Fatalf("negative add should be ignored, got %v", got)
	}
}

func TestHistogramBucketsAreCumulativeAndMatchCount(t *testing.T) {
	r := NewRegistry()
	hv := &HistogramVec{
		desc:     desc{name: "h_seconds", help: "h", typ: "histogram", labelNames: []string{"op"}},
		buckets:  []float64{0.1, 1, 10},
		children: map[string]*Histogram{},
	}
	r.Register(hv)
	h := hv.With("q")
	for _, v := range []float64{0.05, 0.5, 0.5, 5, 50} {
		h.Observe(v)
	}

	out := r.Gather()
	checks := []string{
		`h_seconds_bucket{op="q",le="0.1"} 1`,  // 0.05
		`h_seconds_bucket{op="q",le="1"} 3`,    // + 0.5, 0.5
		`h_seconds_bucket{op="q",le="10"} 4`,   // + 5
		`h_seconds_bucket{op="q",le="+Inf"} 5`, // + 50
		`h_seconds_count{op="q"} 5`,
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Fatalf("missing %q in:\n%s", c, out)
		}
	}
	// +Inf bucket must equal _count — Prometheus rejects the series otherwise.
	if h.Count() != 5 {
		t.Fatalf("count = %d, want 5", h.Count())
	}
	if h.Sum() != 56.05 {
		t.Fatalf("sum = %v, want 56.05", h.Sum())
	}
}

func TestHistogramBoundaryValueGoesInLowerBucket(t *testing.T) {
	// Prometheus buckets are "less than or equal to", so an observation
	// exactly on a boundary belongs to that bucket, not the next one.
	hv := &HistogramVec{
		desc:     desc{name: "b_seconds", typ: "histogram"},
		buckets:  []float64{1, 2},
		children: map[string]*Histogram{},
	}
	h := hv.With()
	h.Observe(1.0)
	if h.counts[0] != 1 {
		t.Fatalf("value 1.0 should land in the le=1 bucket, counts=%v", h.counts)
	}
}

func TestLabelEscaping(t *testing.T) {
	r := NewRegistry()
	cv := &CounterVec{
		desc:     desc{name: "esc_total", typ: "counter", labelNames: []string{"msg"}},
		children: map[string]*Counter{},
	}
	r.Register(cv)
	cv.With(`say "hi"\n`).Inc()
	out := r.Gather()
	if !strings.Contains(out, `msg="say \"hi\"\\n"`) {
		t.Fatalf("label not escaped:\n%s", out)
	}
}

func TestConcurrentCounterIsExact(t *testing.T) {
	cv := &CounterVec{
		desc:     desc{name: "race_total", typ: "counter", labelNames: []string{"k"}},
		children: map[string]*Counter{},
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				cv.With("k1").Inc()
			}
		}()
	}
	wg.Wait()
	if got := cv.With("k1").Value(); got != 10000 {
		t.Fatalf("lost updates: got %v, want 10000", got)
	}
}

func TestWindowQuantileAndRate(t *testing.T) {
	w := NewWindow(1000)
	for i := 1; i <= 100; i++ {
		w.Observe(float64(i))
	}
	if got := w.Quantile(time.Minute, 0.95); got != 95 {
		t.Fatalf("p95 = %v, want 95", got)
	}
	if got := w.Quantile(time.Minute, 0.5); got != 50 {
		t.Fatalf("p50 = %v, want 50", got)
	}
	if got := w.Count(time.Minute); got != 100 {
		t.Fatalf("count = %d, want 100", got)
	}
	// 100 samples over a 2-minute window = 50/min.
	if got := w.Rate(2 * time.Minute); got != 50 {
		t.Fatalf("rate = %v, want 50", got)
	}
}

func TestWindowExcludesOldSamples(t *testing.T) {
	w := NewWindow(100)
	old := time.Now().Add(-10 * time.Minute)
	w.ObserveAt(old, 999)
	w.Observe(1)
	if got := w.Count(time.Minute); got != 1 {
		t.Fatalf("old sample not excluded: count = %d", got)
	}
	if got := w.Mean(time.Minute); got != 1 {
		t.Fatalf("mean = %v, want 1", got)
	}
}

func TestWindowEmptyReturnsZeroNotNaN(t *testing.T) {
	w := NewWindow(10)
	if got := w.Mean(time.Minute); got != 0 {
		t.Fatalf("empty mean = %v, want 0", got)
	}
	if got := w.Quantile(time.Minute, 0.95); got != 0 {
		t.Fatalf("empty quantile = %v, want 0", got)
	}
}

func TestWindowRingBufferWrapsWithoutPanic(t *testing.T) {
	w := NewWindow(8)
	for i := 0; i < 100; i++ {
		w.Observe(float64(i))
	}
	if got := w.Count(time.Minute); got != 8 {
		t.Fatalf("capped window should hold 8, got %d", got)
	}
}

func TestTemplateRoute(t *testing.T) {
	cases := map[string]string{
		"/api/review/9f3c1e2a-4b5d-6e7f-8a9b-0c1d2e3f4a5b": "/api/review/{id}",
		"/api/documents/482/pages/3":                       "/api/documents/{id}/pages/{id}",
		"/api/coupons/SUMMER-4KQ9X2":                       "/api/coupons/{code}",
		"/api/auth/login":                                  "/api/auth/login",
		"/health":                                          "/health",
		"/api/payment/calculate":                           "/api/payment/calculate",
		"/api/extractions/a1b2c3d4e5f60718/complete":       "/api/extractions/{id}/complete",
	}
	for in, want := range cases {
		if got := TemplateRoute(in); got != want {
			t.Errorf("TemplateRoute(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTemplateRouteDoesNotEatRealWords(t *testing.T) {
	// "calculate" and "webhook" are lowercase so the coupon matcher skips
	// them; "SCHEMAS" is uppercase but has no digit.
	for _, p := range []string{"/api/payment/calculate", "/api/SCHEMAS", "/api/payment/webhook/stripe"} {
		got := TemplateRoute(p)
		if strings.Contains(got, "{") {
			t.Errorf("TemplateRoute(%q) = %q — templated a real path segment", p, got)
		}
	}
}

func TestRouteCardinalityIsCapped(t *testing.T) {
	routesMu.Lock()
	pathCache = map[string]string{}
	knownList = map[string]struct{}{}
	routesMu.Unlock()
	defer func() {
		routesMu.Lock()
		pathCache = map[string]string{}
		knownList = map[string]struct{}{}
		routesMu.Unlock()
	}()

	for i := 0; i < maxDistinctRoutes+50; i++ {
		TemplateRoute("/junk/" + strings.Repeat("z", i%40+1) + "/" + string(rune('a'+i%26)) + strings.Repeat("q", i))
	}
	routesMu.RLock()
	n := len(knownList)
	nc := len(pathCache)
	routesMu.RUnlock()
	if nc > maxPathCacheEntries {
		t.Fatalf("path memo grew to %d, cap is %d", nc, maxPathCacheEntries)
	}
	if n > maxDistinctRoutes {
		t.Fatalf("route cache grew to %d, cap is %d", n, maxDistinctRoutes)
	}
}

func TestClassifyError(t *testing.T) {
	cases := map[error]string{
		nil:                                 "none",
		context.DeadlineExceeded:            "timeout",
		context.Canceled:                    "canceled",
		errors.New("no rows in result set"): "not_found",
		errors.New("duplicate key value violates unique constraint"): "conflict",
		errors.New("dial tcp: connection refused"):                   "unavailable",
		errors.New("something went sideways"):                        "internal",
		errors.New("429 Too Many Requests"):                          "rate_limited",
	}
	for err, want := range cases {
		if got := ClassifyError(err); got != want {
			t.Errorf("ClassifyError(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestSpanRecordsMetricsAndPropagatesError(t *testing.T) {
	want := errors.New("boom")
	got := Span(context.Background(), "unit", "op", func(ctx context.Context) error {
		if TraceID(ctx) == "" {
			t.Error("span did not put a trace id on the context")
		}
		if SpanID(ctx) == "" {
			t.Error("span did not put a span id on the context")
		}
		return want
	})
	if !errors.Is(got, want) {
		t.Fatalf("Span swallowed the error: %v", got)
	}
	if c := SpanTotal.With("unit", "op", "error").Value(); c != 1 {
		t.Fatalf("span counter = %v, want 1", c)
	}
	if f := SpanInFlight.With("unit", "op").Value(); f != 0 {
		t.Fatalf("in-flight gauge leaked: %v", f)
	}
}

func TestSpanEndIsIdempotent(t *testing.T) {
	sp := StartSpan(context.Background(), "unit", "double")
	sp.End(nil)
	sp.End(nil)
	if c := SpanTotal.With("unit", "double", "ok").Value(); c != 1 {
		t.Fatalf("double End double-counted: %v", c)
	}
	if f := SpanInFlight.With("unit", "double").Value(); f != 0 {
		t.Fatalf("in-flight gauge wrong after double End: %v", f)
	}
}

func TestSpanValueKeepsType(t *testing.T) {
	v, err := SpanValue(context.Background(), "unit", "typed", func(ctx context.Context) (int, error) {
		return 42, nil
	})
	if err != nil || v != 42 {
		t.Fatalf("got (%v, %v), want (42, nil)", v, err)
	}
}

func TestNestedSpansShareTraceID(t *testing.T) {
	outer := StartSpan(context.Background(), "unit", "outer")
	inner := StartSpan(outer.Context(), "unit", "inner")
	if inner.traceID != outer.traceID {
		t.Fatalf("trace id not inherited: %q vs %q", inner.traceID, outer.traceID)
	}
	if inner.parentID != outer.spanID {
		t.Fatalf("parent span id = %q, want %q", inner.parentID, outer.spanID)
	}
	inner.End(nil)
	outer.End(nil)
}

func TestTrackDBRecordsAndClassifies(t *testing.T) {
	before := DBQueries.With("select", "users", "ok").Value()
	err := TrackDB(context.Background(), "select", "users", func(ctx context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if after := DBQueries.With("select", "users", "ok").Value(); after != before+1 {
		t.Fatalf("ok counter not incremented: %v -> %v", before, after)
	}

	_ = TrackDB(context.Background(), "select", "users", func(ctx context.Context) error {
		return errors.New("no rows in result set")
	})
	if c := DBErrors.With("select", "users", "not_found").Value(); c != 1 {
		t.Fatalf("not_found error counter = %v, want 1", c)
	}
}

func TestMiddlewareRecordsStatusAndTraceHeader(t *testing.T) {
	h := Middleware("test-svc")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("nope"))
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	h.ServeHTTP(rec, req)

	if rec.Header().Get(TraceHeader) == "" {
		t.Fatal("middleware did not echo a trace id")
	}
	if c := HTTPRequests.With("test-svc", "GET", "/api/auth/login", "500").Value(); c != 1 {
		t.Fatalf("request counter = %v, want 1", c)
	}
	if c := HTTPErrors.With("test-svc", "/api/auth/login", "server_error").Value(); c != 1 {
		t.Fatalf("server_error counter = %v, want 1", c)
	}
	if f := HTTPInFlight.With("test-svc").Value(); f != 0 {
		t.Fatalf("in-flight leaked: %v", f)
	}
}

func TestMiddlewareAdoptsInboundTraceID(t *testing.T) {
	const given = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var seen string
	h := Middleware("test-svc")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = TraceID(r.Context())
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set(TraceHeader, given)
	h.ServeHTTP(rec, req)

	if seen != given {
		t.Fatalf("trace id not adopted: got %q, want %q", seen, given)
	}
	if rec.Header().Get(TraceHeader) != given {
		t.Fatalf("response header = %q, want %q", rec.Header().Get(TraceHeader), given)
	}
}

func TestMiddlewareImplicit200(t *testing.T) {
	h := Middleware("implicit")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok")) // no WriteHeader call
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	if c := HTTPRequests.With("implicit", "GET", "/health", "200").Value(); c != 1 {
		t.Fatalf("implicit 200 not recorded: %v", c)
	}
}

func TestMetricsHandlerTokenAuth(t *testing.T) {
	t.Setenv("METRICS_TOKEN", "s3cret")
	h := Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated scrape = %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated scrape = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content type = %q", ct)
	}
}

func TestGatherProducesParseableOutput(t *testing.T) {
	SetBuildInfo("test-svc", "1.2.3")
	HTTPRequests.With("s", "GET", "/x", "200").Inc()
	HTTPDuration.With("s", "GET", "/x").Observe(0.25)

	out := Default.Gather()
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Every sample line must be "name{...} value" — exactly one space
		// separating the series from its value.
		idx := strings.LastIndexByte(line, ' ')
		if idx <= 0 || idx == len(line)-1 {
			t.Fatalf("malformed sample line: %q", line)
		}
		if strings.Contains(line[idx+1:], " ") {
			t.Fatalf("value contains a space: %q", line)
		}
	}
	if !strings.Contains(out, `bondqi_build_info{service="test-svc",version="1.2.3"`) {
		t.Fatalf("build info missing:\n%s", out)
	}
}
