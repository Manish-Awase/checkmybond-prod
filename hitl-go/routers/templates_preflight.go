// routers/templates_preflight.go
//
// Provides ONLY the 3 preflight/drift endpoints below. This does NOT
// include field CRUD (/api/fields) or reconciliation (/api/reconcile) —
// those are referenced by the frontend (api_additions.ts, FieldLibraryPage,
// ReconciliationPage) but have no handler code anywhere in the uploaded
// package. Mounting a TemplatesRouter with just these 3 routes is safe;
// a FieldsRouter/ReconcileRouter still needs to be written before those
// frontend pages will work.
// Preflight/drift handlers — mounted by templates.go's TemplatesRouter().
// This file previously defined its own TemplatesRouter() too; removed to
// avoid a duplicate-function compile error now that both live in one
// router.
package routers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	idb "hitl-platform/internal/db"
	imw "hitl-platform/internal/middleware"
	iqueue "hitl-platform/internal/queue"
)

// handleCheckMatch — preflight fingerprint endpoint.
// Called by the frontend after upload but BEFORE full extraction.
func handleCheckMatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	sessionID := r.URL.Query().Get("session_id")
	pdfPath := r.URL.Query().Get("pdf_path")
	docType := r.URL.Query().Get("doc_type")
	if docType == "" {
		docType = "prospectus"
	}

	if sessionID == "" && pdfPath == "" {
		errJSON(w, http.StatusBadRequest, "session_id or pdf_path required")
		return
	}

	// Resolve pdf_path from the session's documents JSON if needed.
	if sessionID != "" && pdfPath == "" {
		var docsRaw []byte
		err := idb.Pool().QueryRow(ctx,
			`SELECT documents FROM bond_sessions WHERE session_id = $1 AND user_id = $2`,
			sessionID, imw.UserID(r),
		).Scan(&docsRaw)
		if err != nil {
			errJSON(w, http.StatusNotFound, "session not found")
			return
		}
		var docList []map[string]any
		_ = json.Unmarshal(docsRaw, &docList)
		for _, d := range docList {
			if dt, _ := d["doc_type"].(string); dt == docType {
				pdfPath, _ = d["pdf_path"].(string)
				break
			}
		}
	}

	if pdfPath == "" {
		errJSON(w, http.StatusBadRequest, "could not resolve pdf_path")
		return
	}

	// Org isolation: use the caller's own org_id, resolved server-side —
	// never accept an org_id from the request itself, or any user could
	// pass another org's id and read/pollute that org's template cache.
	orgID, err := idb.GetUserOrgID(ctx, imw.UserID(r))
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not resolve user's organization")
		return
	}
	if orgID == "" {
		errJSON(w, http.StatusBadRequest, "user is not assigned to an organization yet")
		return
	}

	preflightID := fmt.Sprintf("preflight_%d", time.Now().UnixNano())
	if err := iqueue.EnqueueBondTask(ctx,
		"workers.celery_app.preflight_fingerprint",
		preflightID, pdfPath, docType, orgID,
	); err != nil {
		errJSON(w, http.StatusInternalServerError, "failed to enqueue preflight")
		return
	}

	if sessionID != "" {
		_, _ = idb.Pool().Exec(ctx,
			`UPDATE bond_sessions SET status = 'preflight', updated_at = now() WHERE session_id = $1`,
			sessionID,
		)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"preflight_id": preflightID,
		"status":       "processing",
		"poll_url":     fmt.Sprintf("/api/templates/preflight-result?preflight_id=%s", preflightID),
	})
}

// handleGetPreflightResult — polling endpoint for preflight status.
func handleGetPreflightResult(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	preflightID := r.URL.Query().Get("preflight_id")
	if preflightID == "" {
		errJSON(w, http.StatusBadRequest, "preflight_id required")
		return
	}

	resultJSON, err := iqueue.GetPreflightResult(ctx, preflightID)
	if err != nil || resultJSON == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"preflight_id": preflightID,
			"status":       "processing",
		})
		return
	}

	var result map[string]any
	_ = json.Unmarshal([]byte(resultJSON), &result)
	writeJSON(w, http.StatusOK, result)
}

// handleGetDriftStats — returns drift metrics for a template, scoped to
// the caller's own org.
func handleGetDriftStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	templateID := chi.URLParam(r, "template_id")

	orgID, err := idb.GetUserOrgID(ctx, imw.UserID(r))
	if err != nil || orgID == "" {
		errJSON(w, http.StatusBadRequest, "user is not assigned to an organization")
		return
	}

	var bankName, docType string
	var successCount, failCount int
	var lastUsed *time.Time
	var createdAt time.Time
	var isActive bool

	err = idb.Pool().QueryRow(ctx,
		`SELECT bank_name, document_type, success_count, fail_count, last_used_at, created_at, is_active
		 FROM template_definitions
		 WHERE template_id = $1 AND org_id = $2`,
		templateID, orgID,
	).Scan(&bankName, &docType, &successCount, &failCount, &lastUsed, &createdAt, &isActive)
	if err != nil {
		errJSON(w, http.StatusNotFound, "template not found")
		return
	}

	total := successCount + failCount
	driftScore := 0.0
	if total > 0 {
		driftScore = float64(failCount) / float64(total)
	}

	status := "healthy"
	if driftScore > 0.3 {
		status = "degraded"
	}
	if driftScore > 0.5 || !isActive {
		status = "unhealthy"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"template_id":   templateID,
		"bank_name":     bankName,
		"document_type": docType,
		"drift": map[string]any{
			"success_count": successCount,
			"fail_count":    failCount,
			"total_uses":    total,
			"drift_score":   roundTo(driftScore, 4),
			"status":        status,
		},
		"last_used_at":   lastUsed,
		"created_at":     createdAt,
		"is_active":      isActive,
		"recommendation": driftRecommendation(driftScore, total),
	})
}

func driftRecommendation(driftScore float64, total int) string {
	if total < 5 {
		return "insufficient_data"
	}
	if driftScore > 0.5 {
		return "retrain_immediately"
	}
	if driftScore > 0.3 {
		return "monitor_closely"
	}
	if driftScore > 0.1 {
		return "periodic_review"
	}
	return "no_action"
}

// roundTo — named to avoid colliding with any existing `round` helper
// elsewhere in the routers package.
func roundTo(v float64, decimals int) float64 {
	shift := 1.0
	for i := 0; i < decimals; i++ {
		shift *= 10
	}
	return float64(int(v*shift+0.5)) / shift
}
