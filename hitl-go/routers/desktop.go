// routers/desktop.go
// API endpoints used exclusively by the Desktop App.
// The desktop app runs Python locally and submits either a finished
// OCR result, or (for LLM-mode schemas) parsed text/tables for the
// server to run the RAG/LLM pipeline on. The original PDF never
// reaches the server in either case.
package routers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	idb "hitl-platform/internal/db"
	imw "hitl-platform/internal/middleware"
	iqueue "hitl-platform/internal/queue"
)

func DesktopRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)

	r.Post("/jobs", handleCreateDesktopJob)
	r.Get("/jobs/{extraction_id}", handleGetDesktopJob)
	r.Post("/jobs/{extraction_id}/result", handleSubmitDesktopResult)
	r.Post("/jobs/{extraction_id}/parsed-content", handleSubmitParsedContent)

	return r
}

// handleCreateDesktopJob reserves an extraction_id on the server.
// The desktop app will process the PDF locally and later either submit
// a finished OCR result, or submit parsed content for LLM-mode schemas.
func handleCreateDesktopJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaID string `json:"schema_id"`
		DocHash  string `json:"doc_hash"`
		Filename string `json:"filename"`
	}
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx := r.Context()
	userID := imw.UserID(r)

	if req.DocHash == "" {
		errJSON(w, http.StatusBadRequest, "doc_hash required")
		return
	}

	extractionID := uuid.New().String()
	slaDeadline := time.Now().Add(24 * time.Hour)

	// Create placeholder job — pdf_path is intentionally empty because
	// the PDF stays on the user's local machine.
	if err := idb.CreateExtractionJob(ctx, extractionID, "", req.SchemaID, req.DocHash, slaDeadline); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not create extraction job")
		return
	}

	// Track ownership via documents table
	docID := uuid.New().String()
	_, _ = idb.Pool().Exec(ctx,
		`INSERT INTO documents (doc_id, doc_hash, filename, pdf_path, schema_path, status, user_id, extraction_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())`,
		docID, req.DocHash, req.Filename, "", "", "processing_local", userID, extractionID,
	)

	writeJSON(w, http.StatusCreated, map[string]any{
		"extraction_id": extractionID,
		"status":        "processing_local",
		"message":       "Process PDF locally, then POST an OCR result to /result or parsed content to /parsed-content",
	})
}

// handleGetDesktopJob lets the desktop app poll for server-side status.
func handleGetDesktopJob(w http.ResponseWriter, r *http.Request) {
	extractionID := chi.URLParam(r, "extraction_id")
	ctx := r.Context()

	job, err := idb.GetExtractionJob(ctx, extractionID)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "db error")
		return
	}
	if job == nil {
		errJSON(w, http.StatusNotFound, "job not found")
		return
	}

	// Verify ownership
	var ownerID string
	_ = idb.Pool().QueryRow(ctx,
		`SELECT user_id FROM documents WHERE extraction_id = $1`,
		extractionID,
	).Scan(&ownerID)
	if ownerID != imw.UserID(r) {
		errJSON(w, http.StatusForbidden, "you do not own this extraction")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"extraction_id": job.ExtractionID,
		"status":        job.Status,
		"result_json":   job.ResultJSON,
		"completed_at":  job.CompletedAt,
	})
}

// handleSubmitDesktopResult receives the JSON output from the local
// Python worker for OCR-mode schemas, which are computed entirely
// client-side.
func handleSubmitDesktopResult(w http.ResponseWriter, r *http.Request) {
	extractionID := chi.URLParam(r, "extraction_id")
	if extractionID == "" {
		errJSON(w, http.StatusBadRequest, "extraction_id required")
		return
	}

	var req struct {
		Status       string `json:"status"`
		FieldResults []any  `json:"field_results"`
	}
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx := r.Context()
	userID := imw.UserID(r)

	// Verify ownership
	var ownerID string
	err := idb.Pool().QueryRow(ctx,
		`SELECT user_id FROM documents WHERE extraction_id = $1`,
		extractionID,
	).Scan(&ownerID)
	if err != nil || ownerID != userID {
		errJSON(w, http.StatusForbidden, "you do not own this extraction")
		return
	}

	resultJSON, _ := json.Marshal(req.FieldResults)
	_, err = idb.Pool().Exec(ctx,
		`UPDATE extraction_jobs
		 SET status = $1, result_json = $2, completed_at = now()
		 WHERE extraction_id = $3`,
		req.Status, string(resultJSON), extractionID,
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not save result")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "complete",
		"extraction_id": extractionID,
	})
}

// handleSubmitParsedContent receives pdfplumber-parsed pages/tables from
// the desktop app for LLM-mode schemas. The desktop app parsed the PDF
// locally (no PDF bytes ever sent); the server persists the parsed
// content and enqueues the RAG/LLM pipeline to run against it, same as
// the website's process_document task but starting from parsed content
// instead of a server-held file.
func handleSubmitParsedContent(w http.ResponseWriter, r *http.Request) {
	extractionID := chi.URLParam(r, "extraction_id")
	if extractionID == "" {
		errJSON(w, http.StatusBadRequest, "extraction_id required")
		return
	}

	var req struct {
		ParsedContent json.RawMessage `json:"parsed_content"`
	}
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.ParsedContent) == 0 {
		errJSON(w, http.StatusBadRequest, "parsed_content required")
		return
	}

	ctx := r.Context()
	userID := imw.UserID(r)

	job, err := idb.GetExtractionJob(ctx, extractionID)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "db error")
		return
	}
	if job == nil {
		errJSON(w, http.StatusNotFound, "job not found")
		return
	}
	if job.SchemaID == nil || job.DocHash == nil {
		errJSON(w, http.StatusBadRequest, "job is missing schema_id or doc_hash")
		return
	}

	var ownerID string
	_ = idb.Pool().QueryRow(ctx,
		`SELECT user_id FROM documents WHERE extraction_id = $1`,
		extractionID,
	).Scan(&ownerID)
	if ownerID != userID {
		errJSON(w, http.StatusForbidden, "you do not own this extraction")
		return
	}

	_, err = idb.Pool().Exec(ctx,
		`UPDATE extraction_jobs
		 SET parsed_content_json = $1, status = 'pending'
		 WHERE extraction_id = $2`,
		string(req.ParsedContent), extractionID,
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not save parsed content")
		return
	}

	if err := iqueue.EnqueueParsedContentJob(ctx, extractionID, *job.SchemaID, *job.DocHash); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not enqueue extraction job")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"extraction_id": extractionID,
		"status":        "processing",
	})
}