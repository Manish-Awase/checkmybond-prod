// routers/queue.go
package routers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	idb "hitl-platform/internal/db"
	imw "hitl-platform/internal/middleware"
)

func QueueRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)

	// Reviewer + Admin: pending/processing jobs ordered by SLA
	r.With(imw.RequireRole("reviewer", "admin")).Get("/", handleGetQueue)

	// Admin only: all jobs
	r.With(imw.RequireRole("admin")).Get("/all", handleGetAllJobs)

	return r
}

func handleGetQueue(w http.ResponseWriter, r *http.Request) {
	jobs, err := idb.ListPendingJobs(r.Context())
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not fetch queue")
		return
	}
	writeJSON(w, http.StatusOK, jobsToResponse(jobs))
}

func handleGetAllJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := idb.ListAllJobs(r.Context())
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not fetch jobs")
		return
	}
	writeJSON(w, http.StatusOK, jobsToResponse(jobs))
}

func jobsToResponse(jobs []idb.ExtractionJob) []map[string]any {
	out := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, map[string]any{
			"id":           j.ExtractionID,
			"doc_hash":     j.DocHash,
			"schema_id":    j.SchemaID,
			"status":       j.Status,
			"pdf_path":     j.PdfPath,
			"error":        j.Error,
			"created_at":   j.CreatedAt,
			"started_at":   j.StartedAt,
			"completed_at": j.CompletedAt,
			"sla_deadline": j.SLADeadline,
		})
	}
	return out
}
