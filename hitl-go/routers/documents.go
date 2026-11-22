// routers/documents.go
package routers

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	idb "hitl-platform/internal/db"
	imw "hitl-platform/internal/middleware"
	iqueue "hitl-platform/internal/queue"
	ireviewqueue "hitl-platform/internal/reviewqueue"
)

const (
	MAX_BATCH_PDFS  = 100
	MAX_QUEUE_DEPTH = 200
	MAX_FILE_SIZE   = 50 << 20
)

// Supported extensions (frontend enforces this too, but we double-check here)
var supportedExts = map[string]bool{
	".pdf": true, ".docx": true, ".doc": true,
	".png": true, ".jpg": true, ".jpeg": true,
	".tiff": true, ".tif": true, ".bmp": true,
	".gif": true, ".webp": true,
}

var uploadDir = func() string {
	d := os.Getenv("UPLOAD_DIR")
	if d == "" {
		d = "uploads"
	}
	os.MkdirAll(d, 0o755)
	return d
}()

func DocumentsRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)
	r.Get("/config", handleDocumentsConfig)
	r.Post("/upload", handleUpload)
	r.Post("/upload-ocr", handleUploadOCR)
	r.Get("/download-corrected", handleDownloadCorrected)
	r.Delete("/session", handleClearSession)
	r.Get("/extraction-status", handleExtractionStatus)
	r.Get("/batch-status", handleBatchStatus)
	r.Get("/session-by-extraction", handleSessionByExtraction)
	r.Post("/bond-upload", handleBondUpload)
	r.Get("/text", handleDocumentText)
	return r
}

func handleDocumentsConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"use_llm": os.Getenv("USE_LLM") == "true",
	})
}

func handleSessionByExtraction(w http.ResponseWriter, r *http.Request) {
	extractionID := r.URL.Query().Get("extraction_id")
	if extractionID == "" {
		errJSON(w, http.StatusBadRequest, "extraction_id query parameter required")
		return
	}

	ctx := r.Context()
	var sessionID, filename string
	err := idb.Pool().QueryRow(ctx,
		`SELECT usf.session_id, d.filename
		 FROM upload_session_files usf
		 JOIN documents d ON d.extraction_id = usf.extraction_id
		 WHERE usf.extraction_id = $1
		 LIMIT 1`,
		extractionID,
	).Scan(&sessionID, &filename)

	if err != nil {
		errJSON(w, http.StatusNotFound, "session not found for this extraction")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"filename":   filename,
	})
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		errJSON(w, http.StatusBadRequest, "multipart parse error — max 50 MB")
		return
	}

	pdfFile, pdfHeader, err := r.FormFile("pdf")
	if err != nil {
		errJSON(w, http.StatusBadRequest, "missing pdf field")
		return
	}
	defer pdfFile.Close()

	pdfBytes, err := io.ReadAll(pdfFile)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not read pdf")
		return
	}

	h := sha256.Sum256(pdfBytes)
	docHash := fmt.Sprintf("%x", h[:])

	pdfName := fmt.Sprintf("%s_%s", uuid.New().String(), filepath.Base(pdfHeader.Filename))
	pdfPath := filepath.Join(uploadDir, pdfName)
	if err := os.WriteFile(pdfPath, pdfBytes, 0o644); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not save pdf")
		return
	}

	schemaFile, schemaHeader, err := r.FormFile("schema")
	if err != nil {
		errJSON(w, http.StatusBadRequest, "missing schema field")
		return
	}
	defer schemaFile.Close()

	schemaBytes, err := io.ReadAll(schemaFile)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not read schema")
		return
	}
	schemaName := fmt.Sprintf("%s_%s", uuid.New().String(), filepath.Base(schemaHeader.Filename))
	schemaPath := filepath.Join(uploadDir, schemaName)
	if err := os.WriteFile(schemaPath, schemaBytes, 0o644); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not save schema")
		return
	}

	sh := sha256.Sum256(schemaBytes)
	schemaID := fmt.Sprintf("schema_%x", sh[:8])

	ctx := r.Context()
	_, _ = idb.Pool().Exec(ctx,
		`INSERT INTO schema_registry (schema_id, schema_name, version, definition_json, created_at, created_by)
		 VALUES ($1, $2, '1.0', $3, now(), $4)
		 ON CONFLICT (schema_id) DO NOTHING`,
		schemaID, schemaHeader.Filename, string(schemaBytes), imw.UserID(r),
	)

	docID := uuid.New().String()
	_, _ = idb.Pool().Exec(ctx,
		`INSERT INTO documents
		 (doc_id, doc_hash, filename, pdf_path, schema_path, status, user_id, extraction_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7, now())`,
		docID, docHash, pdfHeader.Filename, pdfPath, schemaPath, imw.UserID(r), "",
	)

	extractionID := uuid.New().String()
	slaHours := 24
	if v := os.Getenv("SLA_HOURS"); v != "" {
		fmt.Sscanf(v, "%d", &slaHours)
	}
	slaDeadline := time.Now().Add(time.Duration(slaHours) * time.Hour)

	if err := idb.CreateExtractionJob(ctx, extractionID, pdfPath, schemaID, docHash, slaDeadline); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not create extraction job")
		return
	}

	if err := iqueue.EnqueueExtractionJob(ctx, extractionID, pdfPath, schemaID, docHash); err != nil {
		fmt.Printf("[documents] Redis enqueue failed: %v\n", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"extraction_id": extractionID,
		"doc_id":        docID,
		"doc_hash":      docHash,
		"schema_id":     schemaID,
		"status":        "processing",
	})
}

func handleUploadOCR(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	depth, err := idb.GetQueueDepth(ctx)
	if err == nil && depth > MAX_QUEUE_DEPTH {
		errJSON(w, http.StatusTooManyRequests, fmt.Sprintf(
			"queue at capacity (%d pending) — resume when queue clears", depth,
		))
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		errJSON(w, http.StatusBadRequest, "could not parse form")
		return
	}

	sessionID := r.FormValue("session_id")
	isResume := sessionID != ""
	if !isResume {
		sessionID = uuid.New().String()
	}

	applyCorrectionsToAll := r.FormValue("apply_corrections_to_all") == "true"

	regionsFile, _, err := r.FormFile("regions")
	if err != nil {
		errJSON(w, http.StatusBadRequest, "missing regions field")
		return
	}
	defer regionsFile.Close()

	regionsBytes, err := io.ReadAll(io.LimitReader(regionsFile, 1<<20))
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not read regions")
		return
	}

	var regionsCheck []map[string]any
	if err := json.Unmarshal(regionsBytes, &regionsCheck); err != nil {
		errJSON(w, http.StatusBadRequest, "regions must be a valid JSON array")
		return
	}
	if len(regionsCheck) == 0 {
		errJSON(w, http.StatusBadRequest, "regions array is empty")
		return
	}

	pdfHeaders := r.MultipartForm.File["pdfs[]"]
	if len(pdfHeaders) == 0 {
		pdfHeaders = r.MultipartForm.File["pdfs"]
	}
	if len(pdfHeaders) == 0 {
		errJSON(w, http.StatusBadRequest, "no files found")
		return
	}
	if len(pdfHeaders) > MAX_BATCH_PDFS {
		errJSON(w, http.StatusBadRequest, fmt.Sprintf(
			"too many files: %d submitted, max %d per request",
			len(pdfHeaders), MAX_BATCH_PDFS,
		))
		return
	}

	type schemaField struct {
		Name      string         `json:"name"`
		FieldType string         `json:"field_type"`
		Required  bool           `json:"required"`
		Region    map[string]any `json:"region"`
	}
	type syntheticSchema struct {
		SchemaName string        `json:"schema_name"`
		Version    string        `json:"version"`
		Mode       string        `json:"mode"`
		Fields     []schemaField `json:"fields"`
	}

	fields := make([]schemaField, 0, len(regionsCheck))
	for _, reg := range regionsCheck {
		name, _ := reg["field_name"].(string)
		if name == "" {
			name = "unnamed_field"
		}
		fields = append(fields, schemaField{
			Name:      name,
			FieldType: "text",
			Required:  true,
			Region:    reg,
		})
	}

	schema := syntheticSchema{SchemaName: "ocr_regions", Version: "1.0", Mode: "ocr", Fields: fields}
	fieldsPerDoc := len(fields)
	schemaBytes, _ := json.Marshal(schema)
	sh := sha256.Sum256(schemaBytes)
	schemaID := fmt.Sprintf("schema_%x", sh[:8])
	schemaPath := filepath.Join(uploadDir, fmt.Sprintf("ocr_regions_%x.json", sh[:4]))

	if err := os.WriteFile(schemaPath, schemaBytes, 0o644); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not save schema")
		return
	}
	_, _ = idb.Pool().Exec(ctx,
		`INSERT INTO schema_registry (schema_id, schema_name, version, definition_json, created_at, created_by)
		 VALUES ($1, 'ocr_regions', '1.0', $2, now(), $3) ON CONFLICT (schema_id) DO NOTHING`,
		schemaID, string(schemaBytes), imw.UserID(r),
	)

	directProcess := false
	if len(pdfHeaders) == 1 {
		for _, reg := range regionsCheck {
			if cv, ok := reg["correction_value"].(string); ok && cv != "" {
				directProcess = true
				break
			}
		}
	} else if applyCorrectionsToAll && len(pdfHeaders) > 1 {
		for _, reg := range regionsCheck {
			if cv, ok := reg["correction_value"].(string); ok && cv != "" {
				directProcess = true
				break
			}
		}
	}

	userID := imw.UserID(r)

	if !isResume {
		if err := idb.CreateUploadSession(ctx, sessionID, userID, len(pdfHeaders)); err != nil {
			errJSON(w, http.StatusInternalServerError, "could not create session")
			return
		}
	} else {
		var existingTotal int
		err := idb.Pool().QueryRow(ctx,
			`SELECT total_files FROM upload_sessions WHERE session_id = $1`,
			sessionID,
		).Scan(&existingTotal)
		if err != nil {
			if err := idb.CreateUploadSession(ctx, sessionID, userID, len(pdfHeaders)); err != nil {
				errJSON(w, http.StatusInternalServerError, "could not create session")
				return
			}
		}
	}

	alreadyQueued, _ := idb.GetSessionQueuedHashes(ctx, sessionID)

	type fileResult struct {
		Filename     string `json:"filename"`
		FileIndex    int    `json:"file_index"`
		ExtractionID string `json:"extraction_id,omitempty"`
		DocHash      string `json:"doc_hash,omitempty"`
		Status       string `json:"status"`
		Error        string `json:"error,omitempty"`
		SizeKB       int64  `json:"size_kb"`
	}

	results := make([]fileResult, 0, len(pdfHeaders))
	newQueued := 0
	newSkipped := 0
	newFailed := 0
	resumeSkipped := 0

	slaHours := 24
	if v := os.Getenv("SLA_HOURS"); v != "" {
		fmt.Sscanf(v, "%d", &slaHours)
	}

	for i, fh := range pdfHeaders {
		select {
		case <-ctx.Done():
			_ = idb.UpdateSessionCounts(ctx, sessionID, newQueued, newSkipped, newFailed)
			_ = idb.FinalizeSession(ctx, sessionID, "interrupted")
			summary, _ := idb.GetSessionSummary(ctx, sessionID)
			writeJSON(w, http.StatusPartialContent, map[string]any{
				"session_id":  sessionID,
				"interrupted": true,
				"message":     "connection dropped",
				"summary":     summary,
				"jobs":        results,
			})
			return
		default:
		}

		fileID := uuid.New().String()

		f, err := fh.Open()
		if err != nil {
			rec := fileResult{Filename: fh.Filename, FileIndex: i, Status: "failed", Error: "open failed"}
			results = append(results, rec)
			newFailed++
			_ = idb.UpsertSessionFile(ctx, idb.UploadSessionFile{
				ID: fileID, SessionID: sessionID, Filename: fh.Filename,
				FileIndex: i, Status: "failed", Error: &rec.Error,
			})
			continue
		}

		fileName := fmt.Sprintf("%s_%s", uuid.New().String(), filepath.Base(fh.Filename))
		filePath := filepath.Join(uploadDir, fileName)
		outFile, err := os.Create(filePath)
		if err != nil {
			f.Close()
			errMsg := "disk write failed"
			results = append(results, fileResult{Filename: fh.Filename, FileIndex: i, Status: "failed", Error: errMsg})
			newFailed++
			_ = idb.UpsertSessionFile(ctx, idb.UploadSessionFile{
				ID: fileID, SessionID: sessionID, Filename: fh.Filename,
				FileIndex: i, Status: "failed", Error: &errMsg,
			})
			continue
		}

		limited := &io.LimitedReader{R: f, N: MAX_FILE_SIZE + 1}
		written, copyErr := io.Copy(outFile, limited)
		outFile.Close()
		f.Close()

		if limited.N == 0 {
			os.Remove(filePath)
			errMsg := fmt.Sprintf("exceeds %dMB limit", MAX_FILE_SIZE>>20)
			sz := written / 1024
			results = append(results, fileResult{Filename: fh.Filename, FileIndex: i, Status: "skipped", Error: errMsg, SizeKB: sz})
			newSkipped++
			_ = idb.UpsertSessionFile(ctx, idb.UploadSessionFile{
				ID: fileID, SessionID: sessionID, Filename: fh.Filename,
				FileIndex: i, Status: "skipped", Error: &errMsg, SizeBytes: &written,
			})
			continue
		}
		if copyErr != nil {
			os.Remove(filePath)
			errMsg := "read error: " + copyErr.Error()
			results = append(results, fileResult{Filename: fh.Filename, FileIndex: i, Status: "failed", Error: errMsg})
			newFailed++
			_ = idb.UpsertSessionFile(ctx, idb.UploadSessionFile{
				ID: fileID, SessionID: sessionID, Filename: fh.Filename,
				FileIndex: i, Status: "failed", Error: &errMsg, SizeBytes: &written,
			})
			continue
		}

		savedFile, _ := os.Open(filePath)
		hasher := sha256.New()
		io.Copy(hasher, savedFile)
		savedFile.Close()
		docHash := fmt.Sprintf("%x", hasher.Sum(nil))

		if alreadyQueued[docHash] {
			os.Remove(filePath)
			results = append(results, fileResult{
				Filename: fh.Filename, FileIndex: i,
				DocHash: docHash, Status: "resumed",
				SizeKB: written / 1024,
			})
			resumeSkipped++
			continue
		}

		ok, tokErr := idb.DeductTokens(ctx, userID, fieldsPerDoc)
		if tokErr != nil || !ok {
			errMsg := "insufficient tokens"
			results = append(results, fileResult{
				Filename: fh.Filename, FileIndex: i, Status: "skipped_insufficient_tokens",
				Error: errMsg, SizeKB: written / 1024,
			})
			newSkipped++
			_ = idb.UpsertSessionFile(ctx, idb.UploadSessionFile{
				ID: fileID, SessionID: sessionID, Filename: fh.Filename,
				FileIndex: i, Status: "skipped", Error: &errMsg, SizeBytes: &written,
			})
			os.Remove(filePath)
			for j := i + 1; j < len(pdfHeaders); j++ {
				results = append(results, fileResult{
					Filename: pdfHeaders[j].Filename, FileIndex: j,
					Status: "skipped_insufficient_tokens", Error: "insufficient tokens",
				})
				newSkipped++
			}
			break
		}

		docID := uuid.New().String()
		extractionID := uuid.New().String()
		slaDeadline := time.Now().Add(time.Duration(slaHours) * time.Hour)

		_, dbErr := idb.Pool().Exec(ctx,
			`INSERT INTO documents
			 (doc_id, doc_hash, filename, pdf_path, schema_path, status, user_id, extraction_id, created_at)
			 VALUES ($1,$2,$3,$4,$5,'pending',$6,$7,now())`,
			docID, docHash, fh.Filename, filePath, schemaPath, userID, extractionID,
		)
		if dbErr != nil {
			_ = idb.AddTokens(ctx, userID, fieldsPerDoc)
			os.Remove(filePath)
			errMsg := "db insert failed"
			results = append(results, fileResult{Filename: fh.Filename, FileIndex: i, Status: "failed", Error: errMsg})
			newFailed++
			_ = idb.UpsertSessionFile(ctx, idb.UploadSessionFile{
				ID: fileID, SessionID: sessionID, Filename: fh.Filename,
				FileIndex: i, Status: "failed", Error: &errMsg, SizeBytes: &written,
			})
			continue
		}

		// Persist user-provided correction values for audit / review
		for _, reg := range regionsCheck {
			if cv, ok := reg["correction_value"].(string); ok && cv != "" {
				fieldName, _ := reg["field_name"].(string)
				_, _ = idb.Pool().Exec(ctx,
					`INSERT INTO corrections
					 (id, extraction_id, doc_hash, field_name, original_value, corrected_value,
					  failure_type, analyst_id, schema_id, prompt_id, retrieved_chunk_ids,
					  confidence_score, context_relevance, answer_relevance, groundedness, corrected_at)
					 VALUES ($1, $2, $3, $4, NULL, $5, NULL, $6, $7, NULL, NULL, NULL, NULL, NULL, NULL, now())`,
					uuid.New().String(), extractionID, docHash, fieldName, cv, userID, schemaID,
				)
			}
		}

		if err := idb.CreateExtractionJob(ctx, extractionID, filePath, schemaID, docHash, slaDeadline); err != nil {
			_ = idb.AddTokens(ctx, userID, fieldsPerDoc)
			os.Remove(filePath)
			errMsg := "job creation failed"
			results = append(results, fileResult{Filename: fh.Filename, FileIndex: i, Status: "failed", Error: errMsg})
			newFailed++
			_ = idb.UpsertSessionFile(ctx, idb.UploadSessionFile{
				ID: fileID, SessionID: sessionID, Filename: fh.Filename,
				FileIndex: i, Status: "failed", Error: &errMsg, SizeBytes: &written,
			})
			continue
		}

		var enqueueErr error
		if directProcess {
			enqueueErr = iqueue.EnqueueSingleOCRDocument(ctx, extractionID, filePath, schemaID, docHash, string(regionsBytes), i+1)
		} else {
			enqueueErr = iqueue.EnqueueExtractionJob(ctx, extractionID, filePath, schemaID, docHash)
		}

		if enqueueErr != nil {
			_, _ = idb.Pool().Exec(ctx,
				`UPDATE extraction_jobs SET status='failed', error=$1 WHERE extraction_id=$2`,
				"enqueue failed: "+enqueueErr.Error(), extractionID,
			)
			errMsg := "enqueue failed — will retry on worker restart"
			_ = idb.UpsertSessionFile(ctx, idb.UploadSessionFile{
				ID: fileID, SessionID: sessionID, Filename: fh.Filename,
				FileIndex: i, Status: "failed", Error: &errMsg, SizeBytes: &written,
			})
			results = append(results, fileResult{
				Filename: fh.Filename, FileIndex: i, ExtractionID: extractionID,
				Status: "failed", Error: errMsg, SizeKB: written / 1024,
			})
			newFailed++
			continue
		}

		_ = idb.UpsertSessionFile(ctx, idb.UploadSessionFile{
			ID: fileID, SessionID: sessionID, Filename: fh.Filename,
			DocHash: &docHash, FileIndex: i, Status: "queued",
			ExtractionID: &extractionID, SizeBytes: &written,
		})
		alreadyQueued[docHash] = true
		newQueued++
		results = append(results, fileResult{
			Filename: fh.Filename, FileIndex: i,
			ExtractionID: extractionID, DocHash: docHash,
			Status: "queued", SizeKB: written / 1024,
		})
	}

	_ = idb.UpdateSessionCounts(ctx, sessionID, newQueued, newSkipped, newFailed)

	summary, _ := idb.GetSessionSummary(ctx, sessionID)
	allDone := summary != nil && summary["remaining"].(int) == 0
	finalStatus := "in_progress"
	if allDone {
		finalStatus = "complete"
	}
	_ = idb.FinalizeSession(ctx, sessionID, finalStatus)

	tokenBalance, _ := idb.GetTokenBalance(ctx, userID)

	resp := map[string]any{
		"session_id":          sessionID,
		"is_resume":           isResume,
		"new_queued":          newQueued,
		"resume_skipped":      resumeSkipped,
		"skipped":             newSkipped,
		"failed":              newFailed,
		"schema_id":           schemaID,
		"mode":                "ocr",
		"complete":            allDone,
		"summary":             summary,
		"jobs":                results,
		"fields_per_doc":      fieldsPerDoc,
		"token_balance":       tokenBalance,
		"insufficient_tokens": tokenBalance < fieldsPerDoc,
	}
	if directProcess && len(results) > 0 {
		resp["direct_process"] = true
		if len(pdfHeaders) == 1 {
			resp["direct_extraction_id"] = results[0].ExtractionID
		}
	}

	writeJSON(w, http.StatusCreated, resp)
}

func handleExtractionStatus(w http.ResponseWriter, r *http.Request) {
	extractionID := r.URL.Query().Get("extraction_id")
	if extractionID == "" {
		errJSON(w, http.StatusBadRequest, "extraction_id query parameter required")
		return
	}

	ctx := r.Context()
	var status string
	var resultJSON []byte

	err := idb.Pool().QueryRow(ctx,
		`SELECT status, result_json FROM extraction_jobs WHERE extraction_id = $1`,
		extractionID,
	).Scan(&status, &resultJSON)

	if err != nil {
		errJSON(w, http.StatusNotFound, "extraction job not found")
		return
	}

	if (status == "complete" || status == "failed") && len(resultJSON) > 0 {
		var payload map[string]any
		if err := json.Unmarshal(resultJSON, &payload); err == nil {
			writeJSON(w, http.StatusOK, payload)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

func handleBatchStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		errJSON(w, http.StatusBadRequest, "session_id query parameter required")
		return
	}

	ctx := r.Context()
	var total, completed, failed int
	err := idb.Pool().QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE usf.status = 'queued') AS total,
			COUNT(*) FILTER (WHERE ej.status = 'complete') AS completed,
			COUNT(*) FILTER (WHERE ej.status = 'failed') AS failed
		FROM upload_session_files usf
		LEFT JOIN extraction_jobs ej ON ej.extraction_id = usf.extraction_id
		WHERE usf.session_id = $1
	`, sessionID).Scan(&total, &completed, &failed)

	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not query batch status")
		return
	}

	percent := 0
	if total > 0 {
		percent = int(float64(completed+failed) / float64(total) * 100)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":     total,
		"completed": completed,
		"failed":    failed,
		"percent":   percent,
	})
}

func handleDownloadCorrected(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	extractionID := r.URL.Query().Get("extraction_id")

	if sessionID == "" && extractionID == "" {
		errJSON(w, http.StatusBadRequest, "session_id or extraction_id query parameter required")
		return
	}

	ctx := r.Context()
	userID := imw.UserID(r)

	// ── Verify ownership ─────────────────────────────────────────────────
	if sessionID != "" {
		var ownerID string
		err := idb.Pool().QueryRow(ctx,
			`SELECT user_id FROM upload_sessions WHERE session_id = $1`,
			sessionID,
		).Scan(&ownerID)
		if err != nil || ownerID != userID {
			errJSON(w, http.StatusForbidden, "you do not own this session")
			return
		}
	}
	if extractionID != "" {
		var ownerID string
		err := idb.Pool().QueryRow(ctx,
			`SELECT user_id FROM documents WHERE extraction_id = $1`,
			extractionID,
		).Scan(&ownerID)
		if err != nil || ownerID != userID {
			errJSON(w, http.StatusForbidden, "you do not own this extraction")
			return
		}
	}

	exportDir := os.Getenv("EXPORT_DIR")
	if exportDir == "" {
		exportDir = "export"
	}
	pdfDir := filepath.Join(exportDir, "corrected_pdf", "pdf")

	// ── Single extraction download (direct single-file mode) ──────────────
	if extractionID != "" {
		var correctedPath string
		var correctedImagePath string
		var origFilename string

		// 1. Try result_json first (worker writes corrected paths here)
		var resultJSON []byte
		err := idb.Pool().QueryRow(ctx,
			`SELECT result_json FROM extraction_jobs WHERE extraction_id = $1 AND status = 'complete'`,
			extractionID,
		).Scan(&resultJSON)
		if err == nil && len(resultJSON) > 0 {
			var payload map[string]any
			if json.Unmarshal(resultJSON, &payload) == nil {
				if p, ok := payload["corrected_pdf"].(string); ok && p != "" {
					if _, stErr := os.Stat(p); stErr == nil {
						correctedPath = p
					}
				}
				if p, ok := payload["corrected_image"].(string); ok && p != "" {
					if _, stErr := os.Stat(p); stErr == nil {
						correctedImagePath = p
					}
				}
			}
		}

		// 2. Fallback: query documents table for original filename & path
		if correctedPath == "" {
			var origPath string
			err := idb.Pool().QueryRow(ctx,
				`SELECT pdf_path, filename FROM documents WHERE extraction_id = $1`,
				extractionID,
			).Scan(&origPath, &origFilename)
			if err == nil && origPath != "" {
				baseName := filepath.Base(origPath)
				entries, _ := os.ReadDir(pdfDir)
				for _, e := range entries {
					if !e.IsDir() && strings.HasSuffix(e.Name(), "_"+baseName) {
						correctedPath = filepath.Join(pdfDir, e.Name())
						break
					}
				}
			}
		}

		// 3. Last resort: scan directory for the most recent PDF
		if correctedPath == "" {
			entries, _ := os.ReadDir(pdfDir)
			var newestPath string
			var newestTime int64 = -1
			for _, e := range entries {
				if e.IsDir() || filepath.Ext(e.Name()) != ".pdf" {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				if info.ModTime().Unix() > newestTime {
					newestTime = info.ModTime().Unix()
					newestPath = filepath.Join(pdfDir, e.Name())
				}
			}
			correctedPath = newestPath
		}

		if correctedPath == "" {
			errJSON(w, http.StatusNotFound,
				"corrected file not found — extraction may still be processing or corrections were not applied")
			return
		}

		if _, err := os.Stat(correctedPath); err != nil {
			errJSON(w, http.StatusNotFound, "corrected file not found on disk")
			return
		}

		if origFilename == "" {
			_ = idb.Pool().QueryRow(ctx,
				`SELECT filename FROM documents WHERE extraction_id = $1`,
				extractionID,
			).Scan(&origFilename)
		}

		// Determine file extension based on original upload type
		origExt := strings.ToLower(filepath.Ext(origFilename))
		isImageOriginal := origExt == ".png" || origExt == ".jpg" || origExt == ".jpeg" ||
			origExt == ".tiff" || origExt == ".tif" || origExt == ".bmp" ||
			origExt == ".gif" || origExt == ".webp"

		// If original was an image and we have a corrected PNG, serve that directly
		if isImageOriginal && correctedImagePath != "" {
			if _, err := os.Stat(correctedImagePath); err == nil {
				downloadName := "corrected.png"
				if origFilename != "" {
					ext := filepath.Ext(origFilename)
					base := strings.TrimSuffix(origFilename, ext)
					downloadName = base + "_corrected.png"
				}
				w.Header().Set("Content-Type", "image/png")
				w.Header().Set("Content-Disposition", `attachment; filename="`+downloadName+`"`)
				w.Header().Set("X-Content-Type-Options", "nosniff")
				http.ServeFile(w, r, correctedImagePath)
				return
			}
		}

		// Otherwise serve the corrected PDF
		downloadName := "corrected.pdf"
		if origFilename != "" {
			ext := filepath.Ext(origFilename)
			base := strings.TrimSuffix(origFilename, ext)
			if isImageOriginal {
				downloadName = base + "_corrected.pdf"
			} else {
				downloadName = base + "_corrected" + ext
			}
		} else {
			downloadName = "corrected_" + filepath.Base(correctedPath)
		}

		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="`+downloadName+`"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, correctedPath)
		return
	}

	// ── Session-based download (batch / folder mode) ───────────────────────
	rows, err := idb.Pool().Query(ctx,
		`SELECT usf.extraction_id, ej.pdf_path, d.filename,
		        COUNT(DISTINCT ej2.pdf_path) FILTER (WHERE ej2.pdf_path <= ej.pdf_path) AS serial
		 FROM upload_session_files usf
		 JOIN extraction_jobs ej  ON ej.extraction_id = usf.extraction_id
		 JOIN documents d         ON d.extraction_id  = usf.extraction_id
		 LEFT JOIN corrections c  ON c.extraction_id  = usf.extraction_id
		 LEFT JOIN extraction_jobs ej2 ON ej2.extraction_id = c.extraction_id
		 WHERE usf.session_id = $1
		   AND usf.status     = 'queued'
		   AND ej.pdf_path   IS NOT NULL
		 GROUP BY usf.extraction_id, ej.pdf_path, d.filename
		 ORDER BY serial`,
		sessionID,
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not query session files")
		return
	}
	defer rows.Close()

	type fileEntry struct {
		correctedName string
		correctedPath string
	}
	var entries []fileEntry

	for rows.Next() {
		var extractionID, pdfPath, origFilename string
		var serial int64
		if err := rows.Scan(&extractionID, &pdfPath, &origFilename, &serial); err != nil {
			continue
		}
		if serial == 0 {
			serial = 1
		}

		baseName := filepath.Base(pdfPath)
		serialPrefix := strconv.FormatInt(serial, 10) + "_"

		entriesDir, _ := os.ReadDir(pdfDir)
		for _, e := range entriesDir {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, serialPrefix) && strings.HasSuffix(name, "_"+baseName) {
				ext := filepath.Ext(origFilename)
				if ext == "" {
					ext = ".pdf"
				}
				base := strings.TrimSuffix(origFilename, ext)
				zipName := base + "_" + strconv.FormatInt(serial, 10) + "_corrected" + ext

				entries = append(entries, fileEntry{
					correctedName: zipName,
					correctedPath: filepath.Join(pdfDir, name),
				})
				break
			}
		}
	}

	if len(entries) == 0 {
		errJSON(w, http.StatusNotFound,
			"no corrected files found for this session — corrections may not have been submitted yet")
		return
	}

	zipName := "corrected_files_" + sessionID[:8] + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+zipName+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")

	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, e := range entries {
		f, err := os.Open(e.correctedPath)
		if err != nil {
			continue
		}
		fw, err := zw.Create(e.correctedName)
		if err != nil {
			f.Close()
			continue
		}
		io.Copy(fw, f)
		f.Close()
	}
}

func handleClearSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		errJSON(w, http.StatusBadRequest, "session_id query parameter required")
		return
	}

	ctx := r.Context()
	userID := imw.UserID(r)

	var ownerID string
	err := idb.Pool().QueryRow(ctx,
		`SELECT user_id FROM upload_sessions WHERE session_id = $1`,
		sessionID,
	).Scan(&ownerID)
	if err != nil {
		errJSON(w, http.StatusNotFound, "session not found")
		return
	}
	if ownerID != userID {
		errJSON(w, http.StatusForbidden, "you do not own this session")
		return
	}

	exportDir := os.Getenv("EXPORT_DIR")
	if exportDir == "" {
		exportDir = "export"
	}
	pdfDir := filepath.Join(exportDir, "corrected_pdf", "pdf")

	rows, err := idb.Pool().Query(ctx,
		`SELECT ej.pdf_path,
		        COUNT(DISTINCT ej2.pdf_path) FILTER (WHERE ej2.pdf_path <= ej.pdf_path) AS serial
		 FROM upload_session_files usf
		 JOIN extraction_jobs ej  ON ej.extraction_id = usf.extraction_id
		 LEFT JOIN corrections c  ON c.extraction_id  = usf.extraction_id
		 LEFT JOIN extraction_jobs ej2 ON ej2.extraction_id = c.extraction_id
		 WHERE usf.session_id = $1
		   AND ej.pdf_path   IS NOT NULL
		 GROUP BY ej.pdf_path
		 ORDER BY serial`,
		sessionID,
	)

	deletedPDFs := 0
	if err == nil {
		for rows.Next() {
			var pdfPath string
			var serial int64
			if err := rows.Scan(&pdfPath, &serial); err != nil {
				continue
			}
			if serial == 0 {
				serial = 1
			}
			correctedName := strconv.FormatInt(serial, 10) + "_" + filepath.Base(pdfPath)
			correctedPath := filepath.Join(pdfDir, correctedName)
			if err := os.Remove(correctedPath); err == nil {
				deletedPDFs++
			}
			// Also try to delete corresponding PNG if it exists
			pngPath := filepath.Join(pdfDir, correctedName[:len(correctedName)-4]+".png")
			os.Remove(pngPath)
		}
		rows.Close()
	}

	var extractionIDs []string
	idRows, err := idb.Pool().Query(ctx,
		`SELECT DISTINCT extraction_id FROM upload_session_files WHERE session_id = $1`,
		sessionID,
	)
	if err == nil {
		for idRows.Next() {
			var extractionID string
			if idRows.Scan(&extractionID) == nil {
				extractionIDs = append(extractionIDs, extractionID)
			}
		}
		idRows.Close()
	}

	removedFromQueue := 0
	if len(extractionIDs) > 0 {
		n, err := ireviewqueue.RemoveByExtractionIDs(ctx, extractionIDs)
		if err != nil {
			log.Printf("[handleClearSession] queue purge failed for session=%s: %v", sessionID, err)
		} else {
			removedFromQueue = n
		}
	}

	_, err = idb.Pool().Exec(ctx,
		`INSERT INTO cleared_sessions_log
		   (session_id, user_id, total_files, queued_count, skipped_count, failed_count, session_created_at)
		 SELECT session_id, user_id, total_files, queued_count, skipped_count, failed_count, created_at
		 FROM upload_sessions WHERE session_id = $1`,
		sessionID,
	)
	if err != nil {
		log.Printf("[handleClearSession] summary log insert failed for session=%s: %v", sessionID, err)
	}

	_, _ = idb.Pool().Exec(ctx,
		`DELETE FROM upload_session_files WHERE session_id = $1`, sessionID,
	)
	_, _ = idb.Pool().Exec(ctx,
		`DELETE FROM upload_sessions WHERE session_id = $1`, sessionID,
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"cleared":            true,
		"session_id":         sessionID,
		"deleted_pdfs":       deletedPDFs,
		"removed_from_queue": removedFromQueue,
		"message":            "session cleared — corrected files and pending review-queue entries for this session have been removed",
	})
}
