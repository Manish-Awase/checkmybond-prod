// routers/profile.go
//
// Self-service actions a logged-in user takes on their own account:
// leaving feedback/a complaint, and deleting their own account. Both are
// scoped to imw.UserID(r) — never a client-supplied id — same trust model
// as routers/payment.go's PayHandler.
package routers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	idb "hitl-platform/internal/db"
	imetrics "hitl-platform/internal/metrics"
	imw "hitl-platform/internal/middleware"
	iqueue "hitl-platform/internal/queue"
)

func ProfileRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)
	r.Get("/balance", handleGetBalance)
	r.Post("/feedback", handleSubmitFeedback)
	r.Post("/delete-account", handleDeleteAccount)
	return r
}

// Wired as: r.Get("/balance", routers.handleGetBalance) inside ProfileRouter()
//
// Includes active grants (fields still remaining, not yet expired) on
// top of the plain balance total — this is what lets the UI say "your
// tokens expire on DATE" rather than just a bare number. Never-expiring
// grants (free/per-field/bundle/coupon) are omitted entirely by
// idb.ListActiveGrants: there's nothing to warn about for those.
//
// One round trip rather than a second endpoint, since this is always
// read together — nowhere in the frontend wants the balance number
// without also being able to show what's about to lapse.
func handleGetBalance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := imw.UserID(r)

	balance, err := idb.GetTokenBalance(ctx, userID)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not fetch balance")
		return
	}
	grants, err := idb.ListActiveGrants(ctx, userID)
	if err != nil {
		// The balance itself is the load-bearing number — an expiry-list
		// failure shouldn't take the whole response down with it. The
		// user just doesn't see expiry info this one time.
		imetrics.LogCtx(ctx, "error", "list_active_grants_failed", map[string]any{
			"user_id": userID, "error": err.Error(),
		})
		writeJSON(w, http.StatusOK, map[string]any{"token_balance": balance, "expiringGrants": []any{}})
		return
	}

	type expiringGrant struct {
		PlanID          string     `json:"planId"`
		FieldsRemaining int        `json:"fieldsRemaining"`
		GrantedAt       time.Time  `json:"grantedAt"`
		ExpiresAt       *time.Time `json:"expiresAt"`
	}
	out := make([]expiringGrant, 0, len(grants))
	for _, g := range grants {
		out = append(out, expiringGrant{
			PlanID: g.PlanID, FieldsRemaining: g.FieldsRemaining,
			GrantedAt: g.GrantedAt, ExpiresAt: g.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token_balance":  balance,
		"expiringGrants": out, // already sorted soonest-expiring first by ListActiveGrants
	})
}

// ── Feedback / complaints ───────────────────────────────────────────────────

type feedbackRequest struct {
	Kind    string `json:"kind"` // "feedback" | "complaint" — defaults to "feedback"
	Message string `json:"message"`
	Rating  *int   `json:"rating,omitempty"` // optional 1-5
}

// Wired as: r.Post("/feedback", routers.handleSubmitFeedback) inside ProfileRouter()
func handleSubmitFeedback(w http.ResponseWriter, r *http.Request) {
	userID := imw.UserID(r)

	var req feedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Message == "" {
		errJSON(w, http.StatusBadRequest, "message is required")
		return
	}
	if req.Kind != "complaint" {
		req.Kind = "feedback" // only two self-serve kinds; "account_deletion" is set internally, not by this endpoint
	}

	user, err := idb.GetUserByID(r.Context(), userID)
	if err != nil || user == nil {
		errJSON(w, http.StatusNotFound, "user not found")
		return
	}

	id := newID()
	if err := idb.InsertFeedback(r.Context(), idb.Feedback{
		ID: id, UserID: userID, Username: user.Username,
		Kind: req.Kind, Message: req.Message, Rating: req.Rating,
	}); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not save feedback")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "status": "received"})
}

// ── Account deletion ─────────────────────────────────────────────────────

type deleteAccountRequest struct {
	// Optional — captured as feedback (kind="account_deletion") before the
	// account is marked deleted, since this is the one moment you can still
	// ask "why are you leaving" and get an answer.
	Reason string `json:"reason,omitempty"`
	Rating *int   `json:"rating,omitempty"`
}

// handleDeleteAccount soft-deletes (deleted_at + is_active=false) rather
// than removing the row — see idb.SoftDeleteUser's comment for why:
// audit_log/plan_transactions/corrections all FK to users.id.
// Wired as: r.Post("/delete-account", routers.handleDeleteAccount) inside ProfileRouter()
func handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	userID := imw.UserID(r)
	ctx := r.Context()

	var req deleteAccountRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // optional body — a blank reason is fine

	if req.Reason != "" {
		user, err := idb.GetUserByID(ctx, userID)
		if err == nil && user != nil {
			_ = idb.InsertFeedback(ctx, idb.Feedback{
				ID: newID(), UserID: userID, Username: user.Username,
				Kind: "account_deletion", Message: req.Reason, Rating: req.Rating,
			})
		}
	}

	if err := idb.SoftDeleteUser(ctx, userID); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not delete account")
		return
	}

	// Blacklist the current token immediately — same mechanism as
	// handleLogout — so a deleted account can't keep making requests for
	// the rest of the token's natural expiry.
	tokenHash := imw.TokenHash(r)
	tokenExpS := imw.TokenExp(r)
	if tokenHash != "" {
		var ttl int64 = 86400
		if tokenExpS != "" {
			if exp, err := strconv.ParseInt(tokenExpS, 10, 64); err == nil {
				if remaining := exp - time.Now().Unix(); remaining > 0 {
					ttl = remaining
				}
			}
		}
		_ = iqueue.BlacklistToken(ctx, tokenHash, userID, ttl)
	}

	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
