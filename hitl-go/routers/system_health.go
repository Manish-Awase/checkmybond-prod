// routers/system_health.go
//
// Owner-only view of whether the pieces users actually depend on — login,
// data storage, payments — are up and behaving, not just "is the process
// running". Mounted under AccountRouter (see account.go), so it's protected
// by the same HTTP Basic owner auth as /api/account/users, and reachable at
// GET /api/account/health.
package routers

import (
	"context"
	"net"
	"net/http"
	"time"

	idb "hitl-platform/internal/db"
	iqueue "hitl-platform/internal/queue"
)

const (
	requestHealthWindow = 15 * time.Minute
	paymentHealthWindow = 24 * time.Hour
	pingTimeout         = 3 * time.Second

	// Thresholds for the overall "status" field — tune these once you have
	// a feel for normal traffic; they're deliberately conservative to start.
	errorRateDegradedPct   = 2.0
	errorRateUnhealthyPct  = 10.0
	paymentFailDegradedPct = 15.0
	paymentFailUnhealthyPct = 40.0
)

type componentStatus struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
	Detail  string `json:"detail,omitempty"`
}

type systemHealthResponse struct {
	Status     string             `json:"status"` // "healthy" | "degraded" | "unhealthy"
	CheckedAt  time.Time          `json:"checkedAt"`
	Components []componentStatus  `json:"components"`
	Requests   *idb.RequestHealth `json:"requests,omitempty"`   // last 15 min — covers login/auth/data traffic generally
	Payments   *idb.PaymentHealth `json:"payments,omitempty"`   // last 24h — payment-specific, since failures often come back as 200s
}

// handleSystemHealth pings Postgres, Redis, and Qdrant directly (not via
// whatever cached state the app might have) and pulls recent audit_log /
// plan_transactions rows to compute error and payment-failure rates.
// Wired as: r.Get("/health", handleSystemHealth) inside AccountRouter()
func handleSystemHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
	defer cancel()

	components := []componentStatus{}
	allHealthy := true

	if err := idb.PingDB(ctx); err != nil {
		components = append(components, componentStatus{Name: "postgres", Healthy: false, Detail: err.Error()})
		allHealthy = false
	} else {
		components = append(components, componentStatus{Name: "postgres", Healthy: true})
	}

	if err := iqueue.Ping(ctx); err != nil {
		components = append(components, componentStatus{Name: "redis", Healthy: false, Detail: err.Error()})
		allHealthy = false
	} else {
		components = append(components, componentStatus{Name: "redis", Healthy: true})
	}

	if err := pingQdrant(ctx); err != nil {
		components = append(components, componentStatus{Name: "qdrant", Healthy: false, Detail: err.Error()})
		allHealthy = false
	} else {
		components = append(components, componentStatus{Name: "qdrant", Healthy: true})
	}

	// These use r.Context() (not the short ping-timeout ctx) — a couple of
	// aggregate queries can reasonably take a bit longer than a raw ping.
	reqHealth, reqErr := idb.GetRequestHealth(r.Context(), time.Now().Add(-requestHealthWindow))
	payHealth, payErr := idb.GetPaymentHealth(r.Context(), time.Now().Add(-paymentHealthWindow))

	status := "healthy"
	if !allHealthy {
		status = "unhealthy"
	} else if reqErr == nil && reqHealth.ErrorRatePct >= errorRateUnhealthyPct {
		status = "unhealthy"
	} else if payErr == nil && payHealth.FailureRatePct >= paymentFailUnhealthyPct {
		status = "unhealthy"
	} else if (reqErr == nil && reqHealth.ErrorRatePct >= errorRateDegradedPct) ||
		(payErr == nil && payHealth.FailureRatePct >= paymentFailDegradedPct) {
		status = "degraded"
	}

	resp := systemHealthResponse{
		Status:     status,
		CheckedAt:  time.Now().UTC(),
		Components: components,
	}
	if reqErr == nil {
		resp.Requests = reqHealth
	} else {
		components = append(components, componentStatus{Name: "audit_log query", Healthy: false, Detail: reqErr.Error()})
		resp.Components = components
	}
	if payErr == nil {
		resp.Payments = payHealth
	} else {
		components = append(components, componentStatus{Name: "plan_transactions query", Healthy: false, Detail: payErr.Error()})
		resp.Components = components
	}

	writeJSON(w, http.StatusOK, resp)
}

// pingQdrant does a bare TCP dial rather than an HTTP request — Qdrant's
// exact health-check path has changed across versions, but "can we open a
// connection to QDRANT_HOST:QDRANT_PORT at all" answers the question that
// matters here (is the container up and network-reachable) without
// depending on API specifics.
func pingQdrant(ctx context.Context) error {
	host := envOr("QDRANT_HOST", "qdrant")
	port := envOr("QDRANT_PORT", "6333")
	d := net.Dialer{Timeout: pingTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}
