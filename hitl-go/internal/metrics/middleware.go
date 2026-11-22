// internal/metrics/middleware.go
//
// HTTP instrumentation and the /metrics endpoint.
//
// Drop Middleware("hitl-server") into the chi chain and every route is
// measured: request count by status, latency histogram, in-flight gauge,
// response size, and a structured access log carrying the trace id.
package metrics

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Route templating ─────────────────────────────────────────────────────────

// maxDistinctRoutes bounds how many distinct route LABELS this process
// will ever emit. Past the cap, any label not already seen becomes
// "other".
//
// This is a backstop, not the primary defence — templateSegment below
// already collapses ids. But templating is heuristic, and one unexpected
// path shape (say, a path carrying a base64 payload) could otherwise mint
// a new series per request until Prometheus falls over. A hard ceiling
// turns that failure into a slightly less useful graph instead of an
// outage in the monitoring system.
//
// The cap counts labels rather than raw paths, which matters more than it
// sounds: /api/review/<uuid> is millions of raw paths but exactly one
// label. Capping raw paths would let ordinary id-bearing traffic exhaust
// the budget within minutes and push genuinely new routes to "other",
// which is the opposite of the intent.
const maxDistinctRoutes = 300

// maxPathCacheEntries bounds the raw-path→label memo. Independent of the
// label cap above: this one exists only to skip re-splitting strings, so
// when it fills, templating simply recomputes each time. Slightly more
// CPU, no growth, no loss of accuracy.
const maxPathCacheEntries = 4096

var (
	routesMu  sync.RWMutex
	pathCache = map[string]string{}   // raw path -> templated label (memo only)
	knownList = map[string]struct{}{} // the set of labels allowed on metrics
)

// RouteTemplateFunc lets the host application supply a better route name
// than the heuristic below. With chi, set this at startup to use the
// router's own pattern, which is exact rather than guessed:
//
//	metrics.RouteTemplateFunc = func(r *http.Request) string {
//	        if rc := chi.RouteContext(r.Context()); rc != nil {
//	                if p := rc.RoutePattern(); p != "" {
//	                        return p
//	                }
//	        }
//	        return ""
//	}
//
// Return "" to fall back to the heuristic. This is a variable rather than
// a hard chi dependency so this package stays importable from anything,
// including the gateway, which proxies paths it has no routes for.
var RouteTemplateFunc func(r *http.Request) string

// TemplateRoute reduces a concrete request path to a bounded label.
//
//	/api/review/9f3c1e2a-...         -> /api/review/{id}
//	/api/documents/482/pages/3       -> /api/documents/{id}/pages/{id}
//	/api/coupons/SUMMER-4KQ9X2       -> /api/coupons/{code}
func TemplateRoute(path string) string {
	if path == "" {
		return "/"
	}
	routesMu.RLock()
	cached, ok := pathCache[path]
	routesMu.RUnlock()
	if ok {
		return cached
	}

	segments := strings.Split(path, "/")
	for i, seg := range segments {
		segments[i] = templateSegment(seg)
	}
	label := strings.Join(segments, "/")

	routesMu.Lock()
	defer routesMu.Unlock()

	if _, allowed := knownList[label]; !allowed {
		if len(knownList) >= maxDistinctRoutes {
			// Budget spent. Do not memoize this one — the path that
			// overflowed is almost certainly junk, and caching it would
			// let junk evict nothing but still consume memo space.
			return "other"
		}
		knownList[label] = struct{}{}
	}

	if len(pathCache) < maxPathCacheEntries {
		pathCache[path] = label
	}
	return label
}

func templateSegment(seg string) string {
	if seg == "" {
		return seg
	}
	// Pure digits: a numeric id.
	if isAllDigits(seg) {
		return "{id}"
	}
	// UUID, with or without dashes, and any long hex string (doc hashes,
	// token hashes, extraction ids all look like this).
	if looksLikeHexID(seg) {
		return "{id}"
	}
	// Coupon codes: uppercase alphanumerics with optional dashes, at least
	// 6 characters, containing at least one digit. Matched narrowly enough
	// that a real path segment like "SYSTEM" or "REVIEW" is left alone.
	if looksLikeCouponCode(seg) {
		return "{code}"
	}
	return seg
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

func looksLikeHexID(s string) bool {
	stripped := strings.ReplaceAll(s, "-", "")
	if len(stripped) < 16 {
		return false
	}
	digits := 0
	for _, r := range stripped {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	// Require at least one digit so a 16-letter word made only of a–f
	// characters is not mistaken for a hash.
	return digits > 0
}

func looksLikeCouponCode(s string) bool {
	if len(s) < 6 {
		return false
	}
	hasDigit := false
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return hasDigit
}

// ── Middleware ───────────────────────────────────────────────────────────────

// responseRecorder captures what actually went back to the client.
type responseRecorder struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (rr *responseRecorder) WriteHeader(code int) {
	if !rr.wrote {
		rr.status = code
		rr.wrote = true
	}
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	if !rr.wrote {
		// A handler that writes without calling WriteHeader has implicitly
		// sent 200. Recording that here keeps the status label accurate.
		rr.status = http.StatusOK
		rr.wrote = true
	}
	n, err := rr.ResponseWriter.Write(b)
	rr.written += int64(n)
	return n, err
}

// Flush forwards to the underlying writer when it supports flushing. The
// download and streaming endpoints rely on this; without it, wrapping the
// writer would silently disable streaming.
func (rr *responseRecorder) Flush() {
	if f, ok := rr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Middleware instruments every request passing through it.
//
// Mount it EARLY in the chain — before auth, before the audit logger — so
// that requests rejected by authentication still appear in the metrics.
// Otherwise a flood of 401s (the signature of a credential-stuffing run)
// would be invisible, which is the opposite of what you want.
func Middleware(service string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The scrape endpoint measuring itself adds nothing and makes
			// every graph include the monitoring system's own traffic.
			if r.URL.Path == metricsPath() {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()

			// Adopt an inbound trace id if the caller sent one, so the
			// gateway and the server share one id for the same request.
			traceID := r.Header.Get(TraceHeader)
			if traceID == "" {
				traceID = NewTraceID()
			}
			ctx := WithTraceID(r.Context(), traceID)
			r = r.WithContext(ctx)

			// Echo it back: the browser devtools network tab then shows the
			// id to paste into a log search, which turns a user's "it was
			// slow at about 3pm" into an exact lookup.
			w.Header().Set(TraceHeader, traceID)

			route := ""
			if RouteTemplateFunc != nil {
				route = RouteTemplateFunc(r)
			}
			if route == "" {
				route = TemplateRoute(r.URL.Path)
			}

			HTTPInFlight.With(service).Inc()
			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}

			defer func() {
				HTTPInFlight.With(service).Dec()
				elapsed := time.Since(start)
				status := rec.status
				statusLabel := strconv.Itoa(status)

				HTTPRequests.With(service, r.Method, route, statusLabel).Inc()
				HTTPDuration.With(service, r.Method, route).Observe(elapsed.Seconds())
				HTTPResponseSize.With(service, route).Observe(float64(rec.written))

				GetWindow(WinHTTPLatency).Observe(elapsed.Seconds())
				GetWindow(WinHTTPRequests).Observe(1)

				level := "info"
				switch {
				case status >= 500:
					HTTPErrors.With(service, route, "server_error").Inc()
					GetWindow(WinHTTP5xx).Observe(1)
					level = "error"
				case status >= 400:
					HTTPErrors.With(service, route, "client_error").Inc()
					GetWindow(WinHTTP4xx).Observe(1)
					level = "warn"
					if status == http.StatusUnauthorized || status == http.StatusForbidden {
						GetWindow(WinAuthFailures).Observe(1)
					}
				}

				Log(level, "http_request", map[string]any{
					"trace_id":    traceID,
					"method":      r.Method,
					"route":       route,
					"path":        r.URL.Path,
					"status":      status,
					"duration_ms": round2(float64(elapsed.Microseconds()) / 1000),
					"bytes":       rec.written,
					"remote_ip":   clientIP(r),
					"user_id":     r.Header.Get("X-User-Id"),
					"service":     service,
				})
			}()

			next.ServeHTTP(rec, r)
		})
	}
}

func clientIP(r *http.Request) string {
	// The gateway and nginx both sit in front, so the socket address is
	// theirs, not the caller's. Trust the forwarded header for display
	// only — it is a log field, never an authorization input.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if rip := r.Header.Get("X-Real-Ip"); rip != "" {
		return rip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ── /metrics endpoint ────────────────────────────────────────────────────────

func metricsPath() string { return envOr("METRICS_PATH", "/metrics") }

// MetricsPath is the path Handler should be mounted at. Reads
// METRICS_PATH, defaulting to /metrics.
func MetricsPath() string { return metricsPath() }

// Handler serves the Prometheus text exposition of the Default registry.
//
// # ACCESS CONTROL
//
// /metrics leaks operational detail — route names, error rates, user
// counts, revenue counters — so it is not something to leave open on a
// public interface. Two independent guards, both off by default so local
// development is not blocked:
//
//	METRICS_TOKEN=<secret>       require `Authorization: Bearer <secret>`
//	METRICS_ALLOW_CIDR=10.0.0.0/8,172.16.0.0/12
//	                             restrict by source network
//
// In this deployment the simplest correct answer is usually neither: bind
// the scrape port to the private network and never expose /metrics
// through nginx at all. The nginx config shipped in ops/ shows that.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !metricsAuthorized(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(Default.Gather()))
	})
}

func metricsAuthorized(r *http.Request) bool {
	if token := envOr("METRICS_TOKEN", ""); token != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if strings.TrimSpace(got) != token {
			return false
		}
	}
	if cidrs := envOr("METRICS_ALLOW_CIDR", ""); cidrs != "" {
		ip := net.ParseIP(clientIP(r))
		if ip == nil {
			return false
		}
		for _, c := range strings.Split(cidrs, ",") {
			_, network, err := net.ParseCIDR(strings.TrimSpace(c))
			if err != nil {
				continue
			}
			if network.Contains(ip) {
				return true
			}
		}
		return false
	}
	return true
}
