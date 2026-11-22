// routers/coupons.go
//
// The coupon service's HTTP surface, in two halves that live in two
// different places on purpose:
//
//	ADMIN (owner-only, HTTP Basic Auth) — generate, list, inspect,
//	  switch off. Registered in routers/account.go's AccountRouter,
//	  mounted at /api/account/coupons — NOT here, and NOT reachable by
//	  any JWT-authenticated user, admin role included. Coupon
//	  generation controls real money (discount codes) and free token
//	  grants, so it uses the same owner-only Basic Auth gate as the
//	  rest of /api/account — a JWT with role=admin is not enough to
//	  reach it, deliberately: "admin" is a role a regular account can
//	  hold, and this endpoint should not be reachable by compromising
//	  one. The handler functions (handleCouponCreate etc.) still live
//	  in this file; only their route registration moved.
//	USER (any signed-in account, JWT) — check a code, redeem a code,
//	  see your own redemptions. This is what's registered below.
//
// Every user-facing handler takes the user id from imw.UserID(r), which
// reads the gateway-verified X-User-Id header — never from the request
// body. A client-supplied user id would let anyone redeem a coupon into
// someone else's account, or claim a per-user-limited coupon repeatedly
// by sending different ids.
package routers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	icoupon "hitl-platform/internal/coupon"
	idb "hitl-platform/internal/db"
	imetrics "hitl-platform/internal/metrics"
	imw "hitl-platform/internal/middleware"
)

var couponStore *icoupon.Store

// InitCoupons wires the coupon store. Call once from main.go after
// idb.Init:
//
//	routers.InitCoupons(coupon.NewStore(idb.Pool()))
func InitCoupons(store *icoupon.Store) { couponStore = store }

func couponsReady(w http.ResponseWriter) bool {
	if couponStore == nil {
		writeJSONError(w, http.StatusInternalServerError,
			"coupons are not configured — call routers.InitCoupons at startup")
		return false
	}
	return true
}

// couponErrorStatus maps a store error to an HTTP status.
//
// The distinction that matters: a coupon that does not exist, is expired,
// or is already used is a 422, not a 500. These are ordinary outcomes of
// a user typing a code, and returning 500 for them would pollute the
// error-rate alert with normal user behaviour — the monitoring would
// report an outage every time someone mistyped a coupon.
func couponErrorStatus(err error) int {
	switch {
	case errors.Is(err, icoupon.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, icoupon.ErrInactive),
		errors.Is(err, icoupon.ErrExpired),
		errors.Is(err, icoupon.ErrNotStarted),
		errors.Is(err, icoupon.ErrExhausted),
		errors.Is(err, icoupon.ErrAlreadyUsed),
		errors.Is(err, icoupon.ErrPlanNotAllowed),
		errors.Is(err, icoupon.ErrBelowMinimum),
		errors.Is(err, icoupon.ErrWrongKind):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// ── Router ───────────────────────────────────────────────────────────────────

// CouponRouter mounts the user-facing coupon endpoints under /api/coupons
// — validate a code, redeem a code, see your own redemption history.
// Coupon GENERATION and management live in routers/account.go's
// AccountRouter instead (mounted at /api/account/coupons, owner-only —
// see the package comment above for why).
//
// Wire in main.go with:
//
//	r.Mount("/api/coupons", routers.CouponRouter())
//
// No path here is public, so the gateway needs no publicPaths change —
// every route requires a verified identity.
func CouponRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)
	r.Post("/validate", handleCouponValidate)
	r.Post("/redeem", handleCouponRedeem)
	r.Get("/mine", handleMyCoupons)
	return r
}

// ── Admin: create ────────────────────────────────────────────────────────────

type couponCreateRequest struct {
	Code           string   `json:"code"`
	Kind           string   `json:"kind"`
	Value          float64  `json:"value"`
	Currency       string   `json:"currency"`
	AppliesToPlans []string `json:"appliesToPlans"`
	MinAmount      float64  `json:"minAmount"`
	MaxDiscount    *float64 `json:"maxDiscount"`
	MaxRedemptions *int     `json:"maxRedemptions"`
	MaxPerUser     int      `json:"maxRedemptionsPerUser"`
	StartsAt       *string  `json:"startsAt"`  // RFC3339
	ExpiresAt      *string  `json:"expiresAt"` // RFC3339
	Campaign       string   `json:"campaign"`
	Notes          string   `json:"notes"`
	IsActive       *bool    `json:"isActive"`
}

func (req couponCreateRequest) toCoupon(createdBy string) (*icoupon.Coupon, error) {
	c := &icoupon.Coupon{
		Code:           req.Code,
		Kind:           req.Kind,
		Value:          req.Value,
		Currency:       req.Currency,
		AppliesToPlans: req.AppliesToPlans,
		MinAmount:      req.MinAmount,
		MaxDiscount:    req.MaxDiscount,
		MaxRedemptions: req.MaxRedemptions,
		MaxPerUser:     req.MaxPerUser,
		IsActive:       true,
		CreatedBy:      createdBy,
	}
	if req.IsActive != nil {
		c.IsActive = *req.IsActive
	}
	if req.Campaign != "" {
		c.Campaign = &req.Campaign
	}
	if req.Notes != "" {
		c.Notes = &req.Notes
	}
	if req.StartsAt != nil && *req.StartsAt != "" {
		t, err := time.Parse(time.RFC3339, *req.StartsAt)
		if err != nil {
			return nil, errors.New("startsAt must be an RFC3339 timestamp")
		}
		c.StartsAt = t
	} else {
		c.StartsAt = time.Now()
	}
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			return nil, errors.New("expiresAt must be an RFC3339 timestamp")
		}
		c.ExpiresAt = &t
	}
	return c, nil
}

func handleCouponCreate(w http.ResponseWriter, r *http.Request) {
	if !couponsReady(w) {
		return
	}
	var req couponCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	c, err := req.toCoupon("owner")
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := couponStore.Create(r.Context(), c); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	imetrics.CouponsCreated.With(c.Kind, "single").Inc()
	imetrics.LogCtx(r.Context(), "info", "coupon_created", map[string]any{
		"code": c.Code, "kind": c.Kind, "value": c.Value, "created_by": "owner",
	})
	writeJSON(w, http.StatusCreated, c)
}

type couponBulkRequest struct {
	couponCreateRequest
	Count   int    `json:"count"`
	Prefix  string `json:"prefix"`
	CodeLen int    `json:"codeLength"`
	BatchID string `json:"batchId"`
}

func handleCouponBulkCreate(w http.ResponseWriter, r *http.Request) {
	if !couponsReady(w) {
		return
	}
	var req couponBulkRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	template, err := req.toCoupon("owner")
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// Generated codes come from the generator, not the request.
	template.Code = ""
	if req.BatchID != "" {
		template.BatchID = &req.BatchID
	}

	codeLen := req.CodeLen
	if codeLen <= 0 {
		codeLen = 8
	}

	// CreateBulk inserts in chunks of 500, so even the 5,000 ceiling is
	// ten statements and finishes comfortably inside the 60-second
	// request timeout set in cmd/server/main.go. No separate context
	// budget needed.
	ctx := r.Context()

	codes, err := couponStore.CreateBulk(ctx, *template, req.Count, req.Prefix, codeLen)
	if err != nil {
		// Partial success is real here: report what was created so the
		// admin knows the batch is half-made rather than assuming none.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":   err.Error(),
			"created": len(codes),
			"codes":   codes,
			"batchId": template.BatchID,
		})
		return
	}

	imetrics.CouponsCreated.With(template.Kind, "bulk").Add(float64(len(codes)))
	imetrics.LogCtx(ctx, "info", "coupons_bulk_created", map[string]any{
		"count": len(codes), "kind": template.Kind, "batch_id": derefString(template.BatchID),
		"created_by": "owner",
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"created": len(codes),
		"codes":   codes,
		"batchId": template.BatchID,
	})
}

// ── Admin: read and manage ───────────────────────────────────────────────────

func handleCouponList(w http.ResponseWriter, r *http.Request) {
	if !couponsReady(w) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	coupons, err := couponStore.List(r.Context(), icoupon.ListFilter{
		BatchID:    q.Get("batchId"),
		Campaign:   q.Get("campaign"),
		Kind:       q.Get("kind"),
		ActiveOnly: q.Get("activeOnly") == "true",
		Search:     q.Get("search"),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not list coupons")
		return
	}
	writeJSON(w, http.StatusOK, coupons)
}

func handleCouponGet(w http.ResponseWriter, r *http.Request) {
	if !couponsReady(w) {
		return
	}
	c, err := couponStore.Get(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		writeJSONError(w, couponErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"coupon":      c,
		"description": icoupon.Describe(c),
	})
}

func handleCouponRedemptions(w http.ResponseWriter, r *http.Request) {
	if !couponsReady(w) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rs, err := couponStore.ListRedemptions(r.Context(), chi.URLParam(r, "code"), limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not list redemptions")
		return
	}
	writeJSON(w, http.StatusOK, rs)
}

type setActiveRequest struct {
	Active bool `json:"active"`
}

func handleCouponSetActive(w http.ResponseWriter, r *http.Request) {
	if !couponsReady(w) {
		return
	}
	var req setActiveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	code := chi.URLParam(r, "code")
	if err := couponStore.SetActive(r.Context(), code, req.Active); err != nil {
		writeJSONError(w, couponErrorStatus(err), err.Error())
		return
	}
	imetrics.LogCtx(r.Context(), "info", "coupon_active_changed", map[string]any{
		"code": code, "active": req.Active, "by": "owner",
	})
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "active": req.Active})
}

func handleCouponBatchActive(w http.ResponseWriter, r *http.Request) {
	if !couponsReady(w) {
		return
	}
	var req setActiveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	batchID := chi.URLParam(r, "batchId")
	n, err := couponStore.SetBatchActive(r.Context(), batchID, req.Active)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not update the batch")
		return
	}
	imetrics.LogCtx(r.Context(), "warn", "coupon_batch_active_changed", map[string]any{
		"batch_id": batchID, "active": req.Active, "affected": n, "by": "owner",
	})
	writeJSON(w, http.StatusOK, map[string]any{"batchId": batchID, "active": req.Active, "affected": n})
}

func handleCouponDelete(w http.ResponseWriter, r *http.Request) {
	if !couponsReady(w) {
		return
	}
	code := chi.URLParam(r, "code")
	if err := couponStore.Delete(r.Context(), code); err != nil {
		writeJSONError(w, couponErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "deleted": true})
}

func handleCouponStats(w http.ResponseWriter, r *http.Request) {
	if !couponsReady(w) {
		return
	}
	st, err := couponStore.Stats(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not compute coupon stats")
		return
	}
	imetrics.CouponsActive.With().Set(float64(st.ActiveCoupons))
	writeJSON(w, http.StatusOK, st)
}

// ── User: validate ───────────────────────────────────────────────────────────

type couponValidateRequest struct {
	Code   string  `json:"code"`
	PlanID string  `json:"planId"`
	Amount float64 `json:"amount"`
}

// handleCouponValidate previews a code without consuming it.
//
// Returns 200 with valid:false for a code that exists but cannot be used,
// rather than an error status — the checkout page wants to render the
// reason inline ("this coupon has expired"), and treating every rejected
// code as an HTTP error makes that awkward for the client and noisy in
// the metrics.
func handleCouponValidate(w http.ResponseWriter, r *http.Request) {
	if !couponsReady(w) {
		return
	}
	var req couponValidateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Code == "" {
		writeJSONError(w, http.StatusBadRequest, "enter a coupon code")
		return
	}

	// If the caller gave a plan but no amount, price the plan server-side
	// so the discount preview reflects the real order rather than
	// whatever total the client claims.
	amount := req.Amount
	if req.PlanID != "" && amount == 0 {
		if p, ok := plans[req.PlanID]; ok {
			amount = float64(p.Step) * p.PricePerUnit
		}
	}

	eval, err := couponStore.Evaluate(r.Context(), req.Code, imw.UserID(r), req.PlanID, amount)
	if err != nil {
		reason := icoupon.Reason(err)
		imetrics.CouponRejections.With(reason).Inc()
		if errors.Is(err, icoupon.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{
				"valid": false, "reason": err.Error(), "code": req.Code,
			})
			return
		}
		if eval != nil {
			writeJSON(w, http.StatusOK, eval)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "could not check that coupon")
		return
	}

	writeJSON(w, http.StatusOK, eval)
}

// ── User: redeem ─────────────────────────────────────────────────────────────

type couponRedeemRequest struct {
	Code string `json:"code"`
}

// handleCouponRedeem claims a token-kind coupon and credits the balance.
//
// Only 'tokens' coupons are redeemable here. A percent or flat coupon is
// a discount on a purchase, so it is consumed inside the payment flow
// (see PayHandler) where there is an actual order to discount and a
// transaction to attach it to. Redeeming one here would burn the code and
// give the user nothing, so it is refused with an explanation instead.
func handleCouponRedeem(w http.ResponseWriter, r *http.Request) {
	if !couponsReady(w) {
		return
	}
	var req couponRedeemRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Code == "" {
		writeJSONError(w, http.StatusBadRequest, "enter a coupon code")
		return
	}

	ctx := r.Context()
	userID := imw.UserID(r)

	// Look up first so a discount code gets a helpful message rather than
	// being silently consumed.
	c, err := couponStore.Get(ctx, req.Code)
	if err != nil {
		imetrics.CouponRejections.With(icoupon.Reason(err)).Inc()
		writeJSONError(w, couponErrorStatus(err), err.Error())
		return
	}
	if c.Kind != icoupon.KindTokens {
		imetrics.CouponRejections.With("wrong_kind").Inc()
		writeJSONError(w, http.StatusUnprocessableEntity,
			"this is a discount code — enter it at checkout when you buy a plan, and it'll come off the price")
		return
	}

	user, _ := idb.GetUserByID(ctx, userID)
	params := icoupon.RedeemParams{
		Code:   req.Code,
		UserID: userID,
	}
	if user != nil {
		params.Username = user.Username
		params.Email = user.Email
	}

	redemption, coupon, err := couponStore.Redeem(ctx, params)
	imetrics.GetWindow(imetrics.WinCouponRedeems).Observe(1)
	if err != nil {
		reason := icoupon.Reason(err)
		imetrics.CouponRedemptions.With(c.Kind, "rejected").Inc()
		imetrics.CouponRejections.With(reason).Inc()
		imetrics.LogCtx(ctx, "info", "coupon_redeem_rejected", map[string]any{
			"code": icoupon.NormalizeCode(req.Code), "user_id": userID, "reason": reason,
		})
		writeJSONError(w, couponErrorStatus(err), err.Error())
		return
	}

	// Credit the tokens. This is a separate statement from the redemption
	// transaction, so there is a narrow window where the redemption is
	// recorded but the credit failed. That ordering is deliberate: if it
	// were reversed, the failure mode would be free tokens with no record
	// — which is unrecoverable. This way the failure leaves an audit trail
	// naming exactly who to compensate, and the error tells the user to
	// contact support rather than pretending it worked.
	if err := idb.GrantTokens(ctx, userID, "coupon", "coupon", &redemption.ID, redemption.TokensGranted, nil); err != nil {
		imetrics.LogCtx(ctx, "error", "coupon_credit_failed", map[string]any{
			"code": redemption.Code, "user_id": userID,
			"tokens": redemption.TokensGranted, "redemption_id": redemption.ID,
			"error": err.Error(),
		})
		writeJSONError(w, http.StatusInternalServerError,
			"your coupon was accepted but the credit didn't go through — contact support with code "+redemption.ID)
		return
	}

	imetrics.CouponRedemptions.With(coupon.Kind, "accepted").Inc()
	imetrics.CouponTokensGranted.With().Add(float64(redemption.TokensGranted))
	imetrics.TokensGranted.With("coupon").Add(float64(redemption.TokensGranted))
	imetrics.LogCtx(ctx, "info", "coupon_redeemed", map[string]any{
		"code": redemption.Code, "user_id": userID, "tokens": redemption.TokensGranted,
	})

	balance, _ := idb.GetTokenBalance(ctx, userID)
	writeJSON(w, http.StatusOK, map[string]any{
		"code":          redemption.Code,
		"tokensGranted": redemption.TokensGranted,
		"tokenBalance":  balance,
		"description":   icoupon.Describe(coupon),
		"redeemedAt":    redemption.RedeemedAt,
	})
}

func handleMyCoupons(w http.ResponseWriter, r *http.Request) {
	if !couponsReady(w) {
		return
	}
	rs, err := couponStore.ListRedemptionsForUser(r.Context(), imw.UserID(r), 50)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not load your coupons")
		return
	}
	writeJSON(w, http.StatusOK, rs)
}

// ── small helpers ────────────────────────────────────────────────────────────

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
