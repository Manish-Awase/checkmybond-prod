// routers/alerts.go
//
// Admin surface for the alert service: rules, email recipients, incident
// history, and a test-send button.
//
// All admin-only. Alert configuration decides who learns that the system
// is broken, so being able to edit it is equivalent to being able to
// silence it.
package routers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	ialerting "hitl-platform/internal/alerting"
	iemail "hitl-platform/internal/email"
	imetrics "hitl-platform/internal/metrics"
	imw "hitl-platform/internal/middleware"
)

var (
	alertStore  *ialerting.Store
	alertEngine *ialerting.Engine
)

// InitAlerts wires the alert store and engine. Call once from main.go:
//
//	store := alerting.NewStore(idb.Pool())
//	engine := alerting.NewEngine(store, idb.Pool(), notifier, 30*time.Second)
//	engine.Start(ctx)
//	routers.InitAlerts(store, engine)
func InitAlerts(store *ialerting.Store, engine *ialerting.Engine) {
	alertStore = store
	alertEngine = engine
}

func alertsReady(w http.ResponseWriter) bool {
	if alertStore == nil || alertEngine == nil {
		writeJSONError(w, http.StatusInternalServerError,
			"alerting is not configured — call routers.InitAlerts at startup")
		return false
	}
	return true
}

// AlertsRouter mounts every alert endpoint under /api/alerts.
//
//	r.Mount("/api/alerts", routers.AlertsRouter())
func AlertsRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)
	r.Use(imw.RequireRole("admin"))

	// What can be alerted on. Serving the engine's own catalog means the
	// UI's metric dropdown cannot offer something the engine cannot compute.
	r.Get("/catalog", handleAlertCatalog)

	r.Get("/rules", handleListRules)
	r.Post("/rules", handleCreateRule)
	r.Put("/rules/{id}", handleUpdateRule)
	r.Delete("/rules/{id}", handleDeleteRule)

	r.Get("/recipients", handleListRecipients)
	r.Post("/recipients", handleAddRecipient)
	r.Patch("/recipients/{id}/active", handleSetRecipientActive)
	r.Delete("/recipients/{id}", handleDeleteRecipient)

	r.Get("/events", handleListEvents)
	r.Get("/status", handleAlertStatus)

	r.Post("/evaluate", handleForceEvaluate)
	r.Post("/test", handleSendTestAlert)

	return r
}

// ── Catalog ──────────────────────────────────────────────────────────────────

func handleAlertCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ialerting.CatalogList())
}

// ── Rules ────────────────────────────────────────────────────────────────────

func handleListRules(w http.ResponseWriter, r *http.Request) {
	if !alertsReady(w) {
		return
	}
	rules, err := alertStore.ListRules(r.Context(), r.URL.Query().Get("activeOnly") == "true")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not load alert rules")
		return
	}

	// Annotate with live firing state so the UI can show what is wrong
	// right now without a second request.
	firing := alertEngine.FiringSnapshot()
	out := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		entry := map[string]any{"rule": rule, "firing": false}
		if since, ok := firing[rule.ID]; ok {
			entry["firing"] = true
			entry["firingSince"] = since
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, out)
}

func handleCreateRule(w http.ResponseWriter, r *http.Request) {
	if !alertsReady(w) {
		return
	}
	var rule ialerting.Rule
	if err := decodeJSON(r, &rule); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	by := imw.UserID(r)
	rule.CreatedBy = &by

	if err := alertStore.CreateRule(r.Context(), &rule); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	imetrics.LogCtx(r.Context(), "info", "alert_rule_created", map[string]any{
		"rule": rule.Name, "metric": rule.Metric, "by": by,
	})
	writeJSON(w, http.StatusCreated, rule)
}

func handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	if !alertsReady(w) {
		return
	}
	var rule ialerting.Rule
	if err := decodeJSON(r, &rule); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rule.ID = chi.URLParam(r, "id")

	if err := alertStore.UpdateRule(r.Context(), &rule); err != nil {
		if errors.Is(err, ialerting.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	imetrics.LogCtx(r.Context(), "info", "alert_rule_updated", map[string]any{
		"rule": rule.Name, "by": imw.UserID(r),
	})
	writeJSON(w, http.StatusOK, rule)
}

func handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	if !alertsReady(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := alertStore.DeleteRule(r.Context(), id); err != nil {
		if errors.Is(err, ialerting.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "could not delete the rule")
		return
	}
	// Worth a warning-level log: deleting a rule is how monitoring
	// silently disappears, and the audit trail should make that visible.
	imetrics.LogCtx(r.Context(), "warn", "alert_rule_deleted", map[string]any{
		"rule_id": id, "by": imw.UserID(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

// ── Recipients ───────────────────────────────────────────────────────────────

func handleListRecipients(w http.ResponseWriter, r *http.Request) {
	if !alertsReady(w) {
		return
	}
	rs, err := alertStore.ListRecipients(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not load recipients")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recipients": rs,
		// Surfaced so the screen can warn when alerts would only be
		// printed to the server log — the failure that is otherwise
		// discovered during an incident.
		"provider": iemail.AlertProviderName(),
	})
}

func handleAddRecipient(w http.ResponseWriter, r *http.Request) {
	if !alertsReady(w) {
		return
	}
	var rec ialerting.Recipient
	if err := decodeJSON(r, &rec); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rec.IsActive = true

	if err := alertStore.AddRecipient(r.Context(), &rec); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	imetrics.LogCtx(r.Context(), "info", "alert_recipient_added", map[string]any{
		"email": rec.Email, "severity_min": rec.SeverityMin, "by": imw.UserID(r),
	})
	writeJSON(w, http.StatusCreated, rec)
}

func handleSetRecipientActive(w http.ResponseWriter, r *http.Request) {
	if !alertsReady(w) {
		return
	}
	var req setActiveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	id := chi.URLParam(r, "id")
	if err := alertStore.SetRecipientActive(r.Context(), id, req.Active); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "active": req.Active})
}

func handleDeleteRecipient(w http.ResponseWriter, r *http.Request) {
	if !alertsReady(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := alertStore.DeleteRecipient(r.Context(), id); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

// ── Events / status ──────────────────────────────────────────────────────────

func handleListEvents(w http.ResponseWriter, r *http.Request) {
	if !alertsReady(w) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := alertStore.ListEvents(r.Context(), r.URL.Query().Get("state"), limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not load alert history")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// handleAlertStatus is the summary the Alerts screen opens with.
func handleAlertStatus(w http.ResponseWriter, r *http.Request) {
	if !alertsReady(w) {
		return
	}
	ctx := r.Context()
	rules, _ := alertStore.ListRules(ctx, true)
	firing := alertEngine.FiringSnapshot()
	recipients, _ := alertStore.ListRecipients(ctx)

	activeRecipients := 0
	for _, rec := range recipients {
		if rec.IsActive {
			activeRecipients++
		}
	}

	bySeverity := map[string]int{"info": 0, "warning": 0, "critical": 0}
	for _, rule := range rules {
		if _, ok := firing[rule.ID]; ok {
			bySeverity[rule.Severity]++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"activeRules":      len(rules),
		"firingCount":      len(firing),
		"firingBySeverity": bySeverity,
		"activeRecipients": activeRecipients,
		"emailProvider":    iemail.AlertProviderName(),
		// A rule set with nobody to tell is the most common way this
		// subsystem fails, so it is called out explicitly rather than
		// left for someone to infer from two separate numbers.
		"deliverable": activeRecipients > 0,
	})
}

func handleForceEvaluate(w http.ResponseWriter, r *http.Request) {
	if !alertsReady(w) {
		return
	}
	alertEngine.EvaluateAll(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"evaluated": true})
}

type testAlertRequest struct {
	Email string `json:"email"`
}

// handleSendTestAlert proves the mail path works before an incident does.
func handleSendTestAlert(w http.ResponseWriter, r *http.Request) {
	if !alertsReady(w) {
		return
	}
	var req testAlertRequest
	_ = decodeJSON(r, &req) // body is optional; empty means "all recipients"

	if err := alertEngine.SendTest(r.Context(), req.Email); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sent":     true,
		"provider": iemail.AlertProviderName(),
	})
}
