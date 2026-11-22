// routers/documents_bond.go
// Merged into the routers package alongside documents.go — routes are
// registered in DocumentsRouter() (see documents.go). uploadDir and
// MAX_FILE_SIZE below are the package-level values already defined in
// documents.go, reused as-is.

package routers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	idb "hitl-platform/internal/db"
	imw "hitl-platform/internal/middleware"
	iqueue "hitl-platform/internal/queue"
)

func handleBondUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := r.ParseMultipartForm(100 << 20); err != nil {
		errJSON(w, http.StatusBadRequest, "multipart parse error — max 100 MB")
		return
	}

	selectedFieldsJSON := r.FormValue("selected_fields")
	if selectedFieldsJSON == "" {
		errJSON(w, http.StatusBadRequest, "selected_fields required")
		return
	}
	var selectedFieldIDs []string
	if err := json.Unmarshal([]byte(selectedFieldsJSON), &selectedFieldIDs); err != nil {
		errJSON(w, http.StatusBadRequest, "selected_fields must be a JSON array of field IDs")
		return
	}

	pdfFiles := r.MultipartForm.File["pdfs[]"]
	if len(pdfFiles) == 0 {
		pdfFiles = r.MultipartForm.File["pdfs"]
	}
	if len(pdfFiles) == 0 {
		errJSON(w, http.StatusBadRequest, "no PDFs uploaded")
		return
	}
	if len(pdfFiles) > 3 {
		errJSON(w, http.StatusBadRequest, "maximum 3 documents allowed")
		return
	}

	docTypes := []string{"prospectus", "term_sheet", "confirmation"}

	sessionID := uuid.New().String()
	userID := imw.UserID(r)

	_, err := idb.Pool().Exec(ctx,
		`INSERT INTO bond_sessions (session_id, user_id, status, selected_field_ids, created_at, updated_at)
		 VALUES ($1, $2, 'parsing', $3, now(), now())`,
		sessionID, userID, selectedFieldIDs,
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not create session")
		return
	}

	type docInfo struct {
		DocType  string `json:"doc_type"`
		Filename string `json:"filename"`
		PDFPath  string `json:"pdf_path"`
		DocHash  string `json:"doc_hash"`
	}

	docs := []docInfo{}
	for i, fh := range pdfFiles {
		f, err := fh.Open()
		if err != nil {
			continue
		}

		fileName := fmt.Sprintf("%s_%s", uuid.New().String(), filepath.Base(fh.Filename))
		filePath := filepath.Join(uploadDir, fileName)
		outFile, err := os.Create(filePath)
		if err != nil {
			f.Close()
			continue
		}

		limited := &io.LimitedReader{R: f, N: MAX_FILE_SIZE + 1}
		written, _ := io.Copy(outFile, limited)
		outFile.Close()
		f.Close()

		if limited.N == 0 {
			os.Remove(filePath)
			continue
		}

		sf, _ := os.Open(filePath)
		hasher := sha256.New()
		io.Copy(hasher, sf)
		sf.Close()
		docHash := fmt.Sprintf("%x", hasher.Sum(nil))

		docType := docTypes[0]
		if i < len(docTypes) {
			docType = docTypes[i]
		}
		if dt := r.FormValue(fmt.Sprintf("doc_type_%d", i)); dt != "" {
			docType = dt
		}

		docs = append(docs, docInfo{
			DocType:  docType,
			Filename: fh.Filename,
			PDFPath:  filePath,
			DocHash:  docHash,
		})
	}

	docsJSON, _ := json.Marshal(docs)
	_, _ = idb.Pool().Exec(ctx,
		`UPDATE bond_sessions SET documents = $2, updated_at = now() WHERE session_id = $1`,
		sessionID, docsJSON,
	)

	for _, d := range docs {
		iqueue.EnqueueBondTask(ctx, "workers.celery_app.process_bond_documents", sessionID, d.DocType, d.PDFPath, d.DocHash)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"session_id":      sessionID,
		"status":          "parsing",
		"documents":       len(docs),
		"selected_fields": len(selectedFieldIDs),
	})
}

func handleDocumentText(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sessionID := r.URL.Query().Get("session_id")
	docType := r.URL.Query().Get("doc_type")

	if sessionID == "" || docType == "" {
		errJSON(w, http.StatusBadRequest, "session_id and doc_type required")
		return
	}

	var documents []byte
	err := idb.Pool().QueryRow(ctx,
		`SELECT documents FROM bond_sessions WHERE session_id = $1 AND user_id = $2`,
		sessionID, imw.UserID(r),
	).Scan(&documents)
	if err != nil {
		errJSON(w, http.StatusNotFound, "session not found")
		return
	}

	var docs []map[string]any
	json.Unmarshal(documents, &docs)

	for _, d := range docs {
		if dt, _ := d["doc_type"].(string); dt == docType {
			if parsed, ok := d["parsed_json"].(map[string]any); ok && parsed != nil {
				writeJSON(w, http.StatusOK, map[string]any{
					"session_id":  sessionID,
					"doc_type":    docType,
					"filename":    d["filename"],
					"pdf_path":    d["pdf_path"],
					"page_count":  d["page_count"],
					"blocks":      parsed,
				})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"session_id": sessionID,
				"doc_type":   docType,
				"status":     "parsing",
			})
			return
		}
	}

	errJSON(w, http.StatusNotFound, "document type not found in session")
}
