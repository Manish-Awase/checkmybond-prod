// routers/hitl.go
// HITL review queue endpoints.
// Reads directly from extraction_jobs table ordered by SLA deadline.
// No Python SLAQueue dependency — Go does the same logic with SQL.
package routers

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	idb "hitl-platform/internal/db"
	imw "hitl-platform/internal/middleware"
)

func HITLRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)
	r.Use(imw.RequireRole("analyst", "reviewer", "admin"))

	r.Get("/queue",           handleHITLQueue)
	r.Get("/queue/next",      handleHITLNext)

	// Admin only
	r.With(imw.RequireRole("admin")).Post("/escalations/clear", handleClearEscalations)

	return r
}

func handleHITLQueue(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit    := 50
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}

	ctx  := r.Context()
	jobs, err := idb.ListPendingJobs(ctx)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not fetch queue")
		return
	}

	// Trim to limit
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}

	qDepth, _      := idb.GetQueueDepth(ctx)
	escalations, _ := idb.GetEscalationCount(ctx)

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       jobsToResponse(jobs),
		"queue_depth": qDepth,
		"escalations": escalations,
	})
}

func handleHITLNext(w http.ResponseWriter, r *http.Request) {
	ctx  := r.Context()
	jobs, err := idb.ListPendingJobs(ctx)
	if err != nil || len(jobs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"item":    nil,
			"message": "Queue is empty",
		})
		return
	}

	// Return the highest-priority item (oldest SLA deadline = first)
	next := jobs[0]
	writeJSON(w, http.StatusOK, map[string]any{
		"item": map[string]any{
			"id":           next.ExtractionID,
			"doc_hash":     next.DocHash,
			"schema_id":    next.SchemaID,
			"status":       next.Status,
			"sla_deadline": next.SLADeadline,
			"created_at":   next.CreatedAt,
		},
	})
}

func handleClearEscalations(w http.ResponseWriter, r *http.Request) {
	// Mark all past-SLA jobs as acknowledged by updating their SLA deadline
	// so they no longer appear as escalations.
	_, err := idb.Pool().Exec(r.Context(),
		`UPDATE extraction_jobs
		 SET sla_deadline = now() + interval '1 hour'
		 WHERE status IN ('pending','processing') AND sla_deadline < now()`,
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not clear escalations")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}
