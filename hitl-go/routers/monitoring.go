// routers/monitoring.go
package routers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	idb "hitl-platform/internal/db"
	imw "hitl-platform/internal/middleware"
)

func MonitoringRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)
	r.Use(imw.RequireRole("reviewer", "admin"))
	r.Get("/stats", handleStats)
	return r
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	stats, err := idb.GetCorrectionStats(ctx)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not fetch stats")
		return
	}

	drift, err := idb.GetDriftReport(ctx)
	if err != nil {
		drift = []idb.DriftEntry{} // non-fatal
	}

	qDepth, _ := idb.GetQueueDepth(ctx)
	escalations, _ := idb.GetEscalationCount(ctx)

	writeJSON(w, http.StatusOK, map[string]any{
		"correction_stats": stats,
		"drift_report":     drift,
		"queue_depth":      qDepth,
		"escalations":      escalations,
	})
}
