// routers/account.go
package routers

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	idb "hitl-platform/internal/db"
	iqueue "hitl-platform/internal/queue"
)

var (
	ownerUsername = envOrAccount("OWNER_USERNAME", "owner")
	ownerPassword = envOrAccount("OWNER_PASSWORD", "owner@123")
)

func AccountRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(basicAuth)
	r.Get("/users", handleListAllUsers)
	r.Post("/users/{user_id}/toggle", handleToggleUser)
	// System health — DB/Redis/Qdrant reachability + recent error/payment
	// failure rates (routers/system_health.go). Same owner Basic Auth as
	// everything else here.
	r.Get("/health", handleSystemHealth)
	// Every transaction currently marked "refunded" -- set by Stripe's
	// charge.refunded and Razorpay's refund.created/refund.processed
	// webhooks (routers/payment_webhooks.go), across every user. No
	// per-user scoping needed here the way ListTransactionsByUser has --
	// this is owner tooling, not something a regular user's identity
	// should ever gate.
	r.Get("/refunds", handleListRefunds)

	// Coupon generation and management. Deliberately mounted here rather
	// than under the JWT-protected /api/coupons (see routers/coupons.go's
	// package comment) — coupon codes are either a direct discount on a
	// real payment or a free grant of tokens, so creating them needs to
	// require the owner credential specifically, not merely a JWT with
	// role=admin. The handler functions live in coupons.go; only the
	// route registration is here, wrapped in the same basicAuth this
	// whole router already uses.
	r.Route("/coupons", func(r chi.Router) {
		r.Post("/", handleCouponCreate)
		r.Post("/bulk", handleCouponBulkCreate)
		r.Get("/", handleCouponList)
		r.Get("/stats", handleCouponStats)
		r.Patch("/batch/{batchId}/active", handleCouponBatchActive)

		// Registered after the static paths above so chi resolves
		// /stats to its own handler rather than treating it as a
		// {code}.
		r.Get("/{code}", handleCouponGet)
		r.Get("/{code}/redemptions", handleCouponRedemptions)
		r.Patch("/{code}/active", handleCouponSetActive)
		r.Delete("/{code}", handleCouponDelete)
	})

	return r
}

// ── HTTP Basic Auth middleware ─────────────────────────────────────────────────

func basicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Basic ") {
			w.Header().Set("WWW-Authenticate", `Basic realm="owner"`)
			errJSON(w, http.StatusUnauthorized, "owner credentials required")
			return
		}

		decoded, err := base64.StdEncoding.DecodeString(authHeader[6:])
		if err != nil {
			errJSON(w, http.StatusUnauthorized, "malformed credentials")
			return
		}

		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			errJSON(w, http.StatusUnauthorized, "malformed credentials")
			return
		}

		// Timing-safe comparison
		userOK := subtle.ConstantTimeCompare([]byte(parts[0]), []byte(ownerUsername)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(parts[1]), []byte(ownerPassword)) == 1
		if !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="owner"`)
			errJSON(w, http.StatusUnauthorized, "invalid owner credentials")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func handleListAllUsers(w http.ResponseWriter, r *http.Request) {
	users, err := idb.ListUsers(r.Context())
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not fetch users")
		return
	}

	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		var lastLogin *string
		if u.LastLogin != nil {
			s := u.LastLogin.Format(time.RFC3339)
			lastLogin = &s
		}
		out = append(out, map[string]any{
			"user_id":    u.ID,
			"username":   u.Username,
			"email":      u.Email,
			"role":       u.Role,
			"is_active":  u.IsActive,
			"created_at": u.CreatedAt.Format(time.RFC3339),
			"last_login": lastLogin,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total": len(users),
		"active": func() int {
			n := 0
			for _, u := range users {
				if u.IsActive {
					n++
				}
			}
			return n
		}(),
		"users": out,
	})
}

// handleListRefunds returns every transaction currently marked
// "refunded" across every user — see the route comment in AccountRouter
// for where that status actually gets set.
func handleListRefunds(w http.ResponseWriter, r *http.Request) {
	if txStore == nil {
		errJSON(w, http.StatusInternalServerError, "payments are not configured on this server")
		return
	}
	txs, err := txStore.ListRefunds(r.Context())
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not fetch refunds")
		return
	}

	out := make([]map[string]any, 0, len(txs))
	// Summed per-currency, not into one number -- transactions can come
	// from either gateway, in whatever currency each was charged in, and
	// adding, say, $50 to ₹4000 produces a number that means nothing.
	// The sum is of what was actually REFUNDED, not of the original
	// charge: a partial refund of $20 on a $50 purchase counts as $20.
	totalByCurrency := map[string]float64{}
	for _, t := range txs {
		var refundedAt *string
		if t.RefundedAt != nil {
			ts := t.RefundedAt.Format(time.RFC3339)
			refundedAt = &ts
		}
		out = append(out, map[string]any{
			"transaction_id":  t.ID,
			"user_id":         t.UserID,
			"username":        t.Username,
			"email":           t.Email,
			"plan_id":         t.PlanID,
			"plan_label":      t.PlanLabel,
			"total_amount":    t.TotalAmount,
			"refunded_amount": t.RefundedAmount,
			"status":          t.Status, // "refunded" (full) | "partially_refunded"
			"currency":        t.Currency,
			"payment_gateway": t.PaymentGateway,
			"gateway_ref":     t.GatewayTransactionID,
			"created_at":      t.CreatedAt.Format(time.RFC3339),
			"refunded_at":     refundedAt,
		})
		totalByCurrency[t.Currency] += t.RefundedAmount
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"count":           len(txs),
		"totalByCurrency": totalByCurrency,
		"refunds":         out,
	})
}

func handleToggleUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "user_id")
	ctx := r.Context()

	user, err := idb.GetUserByID(ctx, userID)
	if err != nil || user == nil {
		errJSON(w, http.StatusNotFound, "user not found")
		return
	}
	// Matches on the display name, which isn't unique -- see
	// isReservedUsername (auth_identity.go) for why that name can no
	// longer be claimed at signup. Case-insensitive to agree with it.
	if isReservedUsername(user.Username) {
		errJSON(w, http.StatusBadRequest, "cannot toggle the owner account")
		return
	}

	newActive := !user.IsActive
	if err := idb.SetUserActive(ctx, userID, newActive); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not update user")
		return
	}

	if !newActive {
		_ = iqueue.BlacklistUserFlag(ctx, userID)
	} else {
		_ = iqueue.RemoveUserBlacklist(ctx, userID)
	}

	action := "activated"
	if !newActive {
		action = "deactivated"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"toggled":   true,
		"user_id":   userID,
		"username":  user.Username,
		"is_active": newActive,
		"action":    action,
	})
}

func envOrAccount(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
