// routers/templates.go
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

func TemplatesRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)

	r.Post("/annotations", handleSaveAnnotations)
	r.Post("/generate-rules", handleGenerateRules)
	r.Post("/retrain", handleRetrainTemplate)
	r.Get("/", handleListTemplates)
	r.Get("/{id}", handleGetTemplate)

	// Preflight/drift endpoints — handlers defined in templates_preflight.go
	r.Get("/check-match", handleCheckMatch)
	r.Get("/preflight-result", handleGetPreflightResult)
	r.Get("/drift/{template_id}", handleGetDriftStats)

	return r
}

// annotation matches EXACTLY what AnnotationPage.tsx sends — see that
// file's addAnnotation(): {id, field_id, field_name, page, value,
// surrounding_text, section_heading, x, y}. The original upload's struct
// used field_name/field_id/text/page_num/section/evidence, none of which
// match the real frontend payload; fixed to agree with both the frontend
// and services/template_learning.py's corrected field reads.
type annotation struct {
	ID              string  `json:"id"`
	FieldID         string  `json:"field_id"`
	FieldName       string  `json:"field_name"`
	Page            int     `json:"page"`
	Value           string  `json:"value"`
	SurroundingText string  `json:"surrounding_text"`
	SectionHeading  string  `json:"section_heading"`
	X               float64 `json:"x"`
	Y               float64 `json:"y"`
}

func handleSaveAnnotations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		errJSON(w, http.StatusBadRequest, "session_id required")
		return
	}

	var req struct {
		Annotations []annotation `json:"annotations"`
	}
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid json")
		return
	}

	annotationsJSON, _ := json.Marshal(req.Annotations)
	_, err := idb.Pool().Exec(ctx,
		`UPDATE bond_sessions
		 SET annotations = $2, status = 'annotated', updated_at = now()
		 WHERE session_id = $1 AND user_id = $3`,
		sessionID, annotationsJSON, imw.UserID(r),
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not save annotations")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":  sessionID,
		"annotations": len(req.Annotations),
		"status":      "annotated",
	})
}

func handleGenerateRules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		errJSON(w, http.StatusBadRequest, "session_id required")
		return
	}

	var status string
	err := idb.Pool().QueryRow(ctx,
		`SELECT status FROM bond_sessions WHERE session_id = $1 AND user_id = $2`,
		sessionID, imw.UserID(r),
	).Scan(&status)
	if err != nil {
		errJSON(w, http.StatusNotFound, "session not found")
		return
	}

	// Resolve org server-side (never trust a client-supplied org_id) and
	// pass it explicitly to the Celery task — matches the pattern already
	// used in templates_preflight.go's handleCheckMatch, and required
	// since template_learning.py's generate_rules_from_session now takes
	// org_id with no default.
	orgID, err := idb.GetUserOrgID(ctx, imw.UserID(r))
	if err != nil || orgID == "" {
		errJSON(w, http.StatusBadRequest, "user is not assigned to an organization")
		return
	}

	if err := iqueue.EnqueueBondTask(ctx,
		"workers.celery_app.generate_template_rules", sessionID, orgID,
	); err != nil {
		errJSON(w, http.StatusInternalServerError, "failed to enqueue rule generation")
		return
	}

	_, _ = idb.Pool().Exec(ctx,
		`UPDATE bond_sessions SET status = 'learning', updated_at = now() WHERE session_id = $1`, sessionID)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"session_id": sessionID,
		"status":     "learning",
		"message":    "Rule generation started",
	})
}

func handleRetrainTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		TemplateID  string       `json:"template_id"`
		Annotations []annotation `json:"annotations"`
	}
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid json")
		return
	}

	// Ownership check: the template must belong to the caller's org —
	// otherwise any authenticated user could retrain (and corrupt) any
	// other org's cached rules by guessing a template_id.
	orgID, err := idb.GetUserOrgID(ctx, imw.UserID(r))
	if err != nil || orgID == "" {
		errJSON(w, http.StatusBadRequest, "user is not assigned to an organization")
		return
	}
	var ownerOrg string
	err = idb.Pool().QueryRow(ctx,
		`SELECT org_id FROM template_definitions WHERE template_id = $1`, req.TemplateID,
	).Scan(&ownerOrg)
	if err != nil {
		errJSON(w, http.StatusNotFound, "template not found")
		return
	}
	if ownerOrg != orgID {
		errJSON(w, http.StatusForbidden, "template does not belong to your organization")
		return
	}

	annotationsJSON, _ := json.Marshal(req.Annotations)
	if err := iqueue.EnqueueBondTask(ctx,
		"workers.celery_app.retrain_template", req.TemplateID, string(annotationsJSON),
	); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not enqueue retrain")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"template_id": req.TemplateID,
		"status":      "retraining",
	})
}

// handleListTemplates / handleGetTemplate — corrected to the ACTUAL
// TemplateDefinition schema (template_id, fingerprint_json, rules_json,
// field_ids_json, success_count, fail_count) confirmed against
// services/template_matcher.py and template_learning.py's real queries.
// The original upload's SQL used id/fingerprint/field_ids/rules/
// confidence_score/usage_count — columns from a different, discarded
// TemplateDefinition draft (db_additions.py) that nothing else in this
// codebase actually uses.

func handleListTemplates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID, err := idb.GetUserOrgID(ctx, imw.UserID(r))
	if err != nil || orgID == "" {
		errJSON(w, http.StatusBadRequest, "user is not assigned to an organization")
		return
	}

	rows, err := idb.Pool().Query(ctx,
		`SELECT template_id, template_name, bank_name, document_type, fingerprint_json,
		        field_ids_json, success_count, fail_count, is_active, created_at
		 FROM template_definitions WHERE org_id = $1 AND is_active = true
		 ORDER BY last_used_at DESC NULLS LAST`,
		orgID,
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	templates := []map[string]any{}
	for rows.Next() {
		var templateID, bankName, docType string
		var templateName *string
		var fpJSON, fieldIDsJSON []byte
		var successCount, failCount int
		var isActive bool
		var createdAt time.Time

		if err := rows.Scan(&templateID, &templateName, &bankName, &docType, &fpJSON,
			&fieldIDsJSON, &successCount, &failCount, &isActive, &createdAt); err != nil {
			continue
		}
		var fp map[string]any
		_ = json.Unmarshal(fpJSON, &fp)
		var fieldIDs []string
		_ = json.Unmarshal(fieldIDsJSON, &fieldIDs)

		templates = append(templates, map[string]any{
			"template_id":   templateID,
			"template_name": templateName,
			"bank_name":     bankName,
			"document_type": docType,
			"fingerprint":   fp,
			"field_ids":     fieldIDs,
			"success_count": successCount,
			"fail_count":    failCount,
			"is_active":     isActive,
			"created_at":    createdAt,
		})
	}

	writeJSON(w, http.StatusOK, templates)
}

func handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	orgID, err := idb.GetUserOrgID(ctx, imw.UserID(r))
	if err != nil || orgID == "" {
		errJSON(w, http.StatusBadRequest, "user is not assigned to an organization")
		return
	}

	var templateName *string
	var bankName, docType string
	var fpJSON, rulesJSON, fieldIDsJSON []byte
	var successCount, failCount int
	var isActive bool
	var createdAt time.Time

	err = idb.Pool().QueryRow(ctx,
		`SELECT template_name, bank_name, document_type, fingerprint_json, rules_json,
		        field_ids_json, success_count, fail_count, is_active, created_at
		 FROM template_definitions WHERE template_id = $1 AND org_id = $2`,
		id, orgID,
	).Scan(&templateName, &bankName, &docType, &fpJSON, &rulesJSON, &fieldIDsJSON,
		&successCount, &failCount, &isActive, &createdAt)
	if err != nil {
		errJSON(w, http.StatusNotFound, "template not found")
		return
	}

	var fp, rules map[string]any
	_ = json.Unmarshal(fpJSON, &fp)
	_ = json.Unmarshal(rulesJSON, &rules)
	var fieldIDs []string
	_ = json.Unmarshal(fieldIDsJSON, &fieldIDs)

	writeJSON(w, http.StatusOK, map[string]any{
		"template_id":   id,
		"template_name": templateName,
		"bank_name":     bankName,
		"document_type": docType,
		"fingerprint":   fp,
		"rules":         rules,
		"field_ids":     fieldIDs,
		"success_count": successCount,
		"fail_count":    failCount,
		"is_active":     isActive,
		"created_at":    createdAt,
	})
}
