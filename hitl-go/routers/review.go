// routers/review.go
package routers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	idb    "hitl-platform/internal/db"
	imw    "hitl-platform/internal/middleware"
	iqueue "hitl-platform/internal/queue"
)

func ReviewRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)
	r.Post("/correct", handleCorrect)
	return r
}

func handleCorrect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExtractionID   string   `json:"extraction_id"`
		FieldName      string   `json:"field_name"`
		OriginalValue  string   `json:"original_value"`
		CorrectedValue string   `json:"corrected_value"`
		FailureType    string   `json:"failure_type"`
		SchemaID       string   `json:"schema_id"`
		PromptID       string   `json:"prompt_id"`
		ChunkIDs       []string `json:"retrieved_chunk_ids"`
		Confidence     *float64 `json:"confidence_score"`
		ContextRel     *float64 `json:"context_relevance"`
		AnswerRel      *float64 `json:"answer_relevance"`
		Groundedness   *float64 `json:"groundedness"`
	}
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ExtractionID == "" || req.FieldName == "" || req.CorrectedValue == "" {
		errJSON(w, http.StatusUnprocessableEntity, "extraction_id, field_name and corrected_value are required")
		return
	}
	if req.FailureType == "" {
		req.FailureType = "unknown"
	}

	ctx := r.Context()

	// Look up extraction to get doc_hash and pdf_path + original filename
	job, _ := idb.GetExtractionJob(ctx, req.ExtractionID)
	var docHash *string
	var pdfPath, originalFilename string
	if job != nil {
		docHash = job.DocHash
		if job.PdfPath != nil {
			pdfPath = *job.PdfPath
			originalFilename = filepath.Base(pdfPath)
		}
	}

	var schemaID, promptID, chunkStr *string
	if req.SchemaID != "" { schemaID = &req.SchemaID }
	if req.PromptID != "" { promptID = &req.PromptID }
	if len(req.ChunkIDs) > 0 {
		raw, _ := json.Marshal(req.ChunkIDs)
		s := string(raw)
		chunkStr = &s
	}
	orig := req.OriginalValue

	c := &idb.Correction{
		ID:                uuid.New().String(),
		ExtractionID:      req.ExtractionID,
		DocHash:           docHash,
		FieldName:         req.FieldName,
		OriginalValue:     &orig,
		CorrectedValue:    req.CorrectedValue,
		FailureType:       req.FailureType,
		AnalystID:         imw.UserID(r),
		SchemaID:          schemaID,
		PromptID:          promptID,
		RetrievedChunkIDs: chunkStr,
		ConfidenceScore:   req.Confidence,
		ContextRelevance:  req.ContextRel,
		AnswerRelevance:   req.AnswerRel,
		Groundedness:      req.Groundedness,
	}

	if err := idb.CreateCorrection(ctx, c); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not save correction")
		return
	}

	// ── DPO pair export ────────────────────────────────────────────────────────
	exported := exportDPOPair(c)

	// ── Enqueue PDF correction task ────────────────────────────────────────────
	// Determine serial number for this PDF — count how many distinct PDFs have
	// corrections so far (including this one). The serial is stable: once a PDF
	// gets serial number N, every correction to it uses the same N.
	serialNumber := getOrAssignSerialNumber(ctx, pdfPath)

	pdfTaskEnqueued := false
	if pdfPath != "" && req.OriginalValue != "" {
		err := iqueue.EnqueuePDFCorrectionTask(ctx, iqueue.PDFCorrectionArgs{
			CorrectionID:     c.ID,
			ExtractionID:     req.ExtractionID,
			PdfPath:          pdfPath,
			FieldName:        req.FieldName,
			OriginalValue:    req.OriginalValue,
			CorrectedValue:   req.CorrectedValue,
			SerialNumber:     serialNumber,
			OriginalFilename: originalFilename,
		})
		pdfTaskEnqueued = err == nil
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":              "saved",
		"correction_id":       c.ID,
		"dpo_pair_exported":   exported,
		"pdf_correction":      map[string]any{
			"enqueued":      pdfTaskEnqueued,
			"serial_number": serialNumber,
			"output_name":   fmt.Sprintf("%d_%s", serialNumber, originalFilename),
			"output_dir":    "corrected_pdf/pdf/",
		},
	})
}

// getOrAssignSerialNumber returns a stable 1-based serial number for a PDF path.
// The serial is based on how many distinct pdf_paths have corrections — the first
// PDF ever corrected gets 1, the second gets 2, etc. The same PDF always gets
// the same serial regardless of how many fields are corrected on it.
func getOrAssignSerialNumber(ctx context.Context, pdfPath string) int {
	if pdfPath == "" {
		return 1
	}
	var serial int
	err := idb.Pool().QueryRow(ctx,
		// Count how many distinct pdf paths were corrected before this one (inclusive)
		// by joining corrections with extraction_jobs on extraction_id
		`SELECT COUNT(DISTINCT ej.pdf_path)
		 FROM corrections c
		 JOIN extraction_jobs ej ON ej.extraction_id = c.extraction_id
		 WHERE ej.pdf_path <= $1`,
		pdfPath,
	).Scan(&serial)
	if err != nil || serial == 0 {
		// Fallback — count all corrected distinct pdfs and add 1
		_ = idb.Pool().QueryRow(ctx,
			`SELECT COUNT(DISTINCT ej.pdf_path) + 1
			 FROM corrections c
			 JOIN extraction_jobs ej ON ej.extraction_id = c.extraction_id`,
		).Scan(&serial)
	}
	if serial < 1 {
		serial = 1
	}
	return serial
}

// exportDPOPair appends a DPO training pair to export/dpo_pairs.jsonl
func exportDPOPair(c *idb.Correction) bool {
	exportDir := os.Getenv("EXPORT_DIR")
	if exportDir == "" {
		exportDir = "export"
	}
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		return false
	}

	pair := map[string]any{
		"correction_id":   c.ID,
		"extraction_id":   c.ExtractionID,
		"field_name":      c.FieldName,
		"original_value":  c.OriginalValue,
		"corrected_value": c.CorrectedValue,
		"failure_type":    c.FailureType,
		"analyst_id":      c.AnalystID,
		"prompt_id":       c.PromptID,
		"confidence":      c.ConfidenceScore,
		"exported_at":     time.Now().UTC().Format(time.RFC3339),
	}

	line, err := json.Marshal(pair)
	if err != nil {
		return false
	}

	f, err := os.OpenFile(
		filepath.Join(exportDir, "dpo_pairs.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644,
	)
	if err != nil {
		return false
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\n", line)
	return true
}
