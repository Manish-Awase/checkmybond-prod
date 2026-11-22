// routers/reconcile.go
package routers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	idb "hitl-platform/internal/db"
	imw "hitl-platform/internal/middleware"
	iqueue "hitl-platform/internal/queue"
)

func ReconcileRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)

	r.Get("/{session_id}", handleGetReconciliation)
	r.Post("/{session_id}/confirm", handleConfirmReconciliation)
	r.Post("/edit", handleEditExtraction)

	return r
}

func handleGetReconciliation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sessionID := chi.URLParam(r, "session_id")

	var extractionResults, reconciliationResult []byte
	var status string
	err := idb.Pool().QueryRow(ctx,
		`SELECT status, extraction_results, reconciliation_result
		 FROM bond_sessions WHERE session_id = $1 AND user_id = $2`,
		sessionID, imw.UserID(r),
	).Scan(&status, &extractionResults, &reconciliationResult)
	if err != nil {
		errJSON(w, http.StatusNotFound, "session not found")
		return
	}

	if status == "extracted" {
		iqueue.EnqueueBondTask(ctx, "workers.celery_app.run_reconciliation", sessionID)
		_, _ = idb.Pool().Exec(ctx,
			`UPDATE bond_sessions SET status = 'reconciling', updated_at = now() WHERE session_id = $1`, sessionID)

		writeJSON(w, http.StatusAccepted, map[string]any{
			"session_id": sessionID,
			"status":     "reconciling",
			"message":    "Reconciliation in progress",
		})
		return
	}

	var reconciled map[string]any
	if len(reconciliationResult) > 0 {
		json.Unmarshal(reconciliationResult, &reconciled)
	}

	var extracted map[string]any
	if len(extractionResults) > 0 {
		json.Unmarshal(extractionResults, &extracted)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":     sessionID,
		"status":         status,
		"extractions":    extracted,
		"reconciliation": reconciled,
	})
}

func handleConfirmReconciliation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sessionID := chi.URLParam(r, "session_id")

	var req struct {
		ConfirmedValues map[string]any `json:"confirmed_values"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid json")
		return
	}

	confirmedJSON, _ := json.Marshal(req.ConfirmedValues)
	_, err := idb.Pool().Exec(ctx,
		`UPDATE bond_sessions
		 SET reconciliation_result = jsonb_set(
			COALESCE(reconciliation_result,'{}'::jsonb),
			'{confirmed_values}',
			$2::jsonb
		 ),
		 status = 'confirmed',
		 updated_at = now()
		 WHERE session_id = $1 AND user_id = $3`,
		sessionID, confirmedJSON, imw.UserID(r),
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not confirm")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "confirmed"})
}

func handleEditExtraction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		SessionID      string `json:"session_id"`
		FieldName      string `json:"field_name"`
		DocType        string `json:"doc_type"`
		CorrectedValue string `json:"corrected_value"`
		Evidence       string `json:"evidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid json")
		return
	}

	var extractionResults []byte
	err := idb.Pool().QueryRow(ctx,
		`SELECT extraction_results FROM bond_sessions WHERE session_id = $1 AND user_id = $2`,
		req.SessionID, imw.UserID(r),
	).Scan(&extractionResults)
	if err != nil {
		errJSON(w, http.StatusNotFound, "session not found")
		return
	}

	var results map[string]any
	json.Unmarshal(extractionResults, &results)

	if docMap, ok := results[req.DocType].(map[string]any); ok {
		if fieldMap, ok := docMap[req.FieldName].(map[string]any); ok {
			fieldMap["value"] = req.CorrectedValue
			fieldMap["evidence"] = req.Evidence
			fieldMap["edited"] = true
			fieldMap["edited_at"] = time.Now().UTC().Format(time.RFC3339)
		}
	}

	updatedJSON, _ := json.Marshal(results)
	_, err = idb.Pool().Exec(ctx,
		`UPDATE bond_sessions SET extraction_results = $2, updated_at = now() WHERE session_id = $1`,
		req.SessionID, updatedJSON,
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not save edit")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": req.SessionID,
		"field":      req.FieldName,
		"status":     "edited",
	})
}
