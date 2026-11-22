// routers/extractions.go
package routers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	idb "hitl-platform/internal/db"
	imw "hitl-platform/internal/middleware"
)

func ExtractionsRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)
	r.Get("/{id}", handleGetExtraction)
	r.Post("/{extractionID}/complete", handleMarkExtractionComplete)
	return r
}

func handleGetExtraction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	job, err := idb.GetExtractionJob(ctx, id)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "db error")
		return
	}
	if job == nil {
		errJSON(w, http.StatusNotFound, "extraction not found")
		return
	}

	// Parse field results Python wrote as JSON
	var fields any = []any{}
	if job.ResultJSON != nil && *job.ResultJSON != "" {
		parsed, err := idb.ParseFieldResults(*job.ResultJSON)
		if err == nil {
			fields = parsed
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"extraction_id": job.ExtractionID,
		"status":        job.Status,
		"fields":        fields,
		"error":         job.Error,
		"created_at":    job.CreatedAt,
		"started_at":    job.StartedAt,
		"completed_at":  job.CompletedAt,
		"sla_deadline":  job.SLADeadline,
	})
}

func handleMarkExtractionComplete(w http.ResponseWriter, r *http.Request) {
	extractionID := chi.URLParam(r, "extractionID")
	if extractionID == "" {
		errJSON(w, http.StatusBadRequest, "extraction_id required")
		return
	}

	ctx := r.Context()
	res, err := idb.Pool().Exec(ctx,
		`UPDATE extraction_jobs
		 SET status = 'complete', completed_at = now()
		 WHERE extraction_id = $1`,
		extractionID,
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not mark extraction complete")
		return
	}

	if res.RowsAffected() == 0 {
		errJSON(w, http.StatusNotFound, "extraction job not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "complete"})
}
