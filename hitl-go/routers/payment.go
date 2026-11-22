// routers/payment.go
package routers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	icoupon "hitl-platform/internal/coupon"
	idb "hitl-platform/internal/db"
	"hitl-platform/internal/interfaces"
	imetrics "hitl-platform/internal/metrics"
	imw "hitl-platform/internal/middleware"
	iplanstore "hitl-platform/internal/planstore"
)

// plan mirrors the plans offered on the frontend's SelectPlanPage.
type plan struct {
	ID            string  `json:"id"`
	Label         string  `json:"label"`
	PricePerUnit  float64 `json:"pricePerUnit"`
	UnitLabel     string  `json:"unitLabel"`
	Step          int     `json:"step"`
	FieldsPerUnit int     `json:"fieldsPerUnit"`
	// ExpiresAfter is nil for a grant that never expires (free,
	// per-field, bundle). "monthly" and "annual" set this to 30 or 365
	// days: their fields ARE time-boxed even though the purchase itself
	// is one-time, not an auto-renewing subscription — see the comment
	// on the plans map below for why those aren't the same thing.
	ExpiresAfter *time.Duration `json:"-"`
}

// expiresAt computes the actual timestamp a grant under this plan should
// lapse at, or nil if it never does. Centralized here rather than
// inlined at every AddTokens→GrantTokens call site, so "when exactly
// does a grant expire" has one answer.
func (p plan) expiresAt(grantedAt time.Time) *time.Time {
	if p.ExpiresAfter == nil {
		return nil
	}
	t := grantedAt.Add(*p.ExpiresAfter)
	return &t
}

// plans is the single source of truth for pricing. Keep in sync with
// frontend/src/pages/SelectPlanPage.tsx if you change anything here.
//
// "monthly"/"annual" are ONE-TIME bundle purchases (no subscriptions
// table, no recurring-billing webhook handling, no auto-renewal, no
// auto-charge) -- but unlike the plans below, their fields DO expire:
// 30 or 365 days after the purchase (ExpiresAfter), tracked per-grant in
// token_grants and reclaimed by the sweep job
// (idb.SweepExpiredGrants, started in cmd/server/main.go). Free,
// per-field, and bundle grants never expire. If real auto-renewing
// subscriptions are wanted later, that's still separate, materially
// more work: a subscriptions table and Stripe/Razorpay's Subscriptions
// API (not Charge()).
var (
	oneMonth = 30 * 24 * time.Hour
	oneYear  = 365 * 24 * time.Hour
)

var plans = map[string]plan{
	"free": {
		ID: "free", Label: "Free",
		PricePerUnit: 0, UnitLabel: "field", Step: 1, FieldsPerUnit: 1,
	},
	"per-field": {
		ID: "per-field", Label: "Pay per field",
		// Step raised from 1 to 10: at $0.50/field and Stripe's ~2.9%+$0.30
		// fee, a single-field ($0.50) purchase loses ~63% of revenue to
		// the processor alone before any LLM cost. At step=10 ($5 min),
		// the fee is ~9% of revenue -- still worth knowing about, no
		// longer a purchase that can lose money outright.
		PricePerUnit: 0.5, UnitLabel: "field", Step: 10, FieldsPerUnit: 1,
	},
	"per-3-field": {
		ID: "per-3-field", Label: "Bundle plan",
		PricePerUnit: 1, UnitLabel: "bundle of 3 fields", Step: 10, FieldsPerUnit: 3,
	},
	"monthly": {
		// $50 / 200 fields = $0.25/field -- half the per-field rate.
		// Starting numbers, not calibrated against any market data --
		// adjust freely.
		ID: "monthly", Label: "Monthly",
		PricePerUnit: 50, UnitLabel: "monthly plan", Step: 1, FieldsPerUnit: 200,
		ExpiresAfter: &oneMonth,
	},
	"annual": {
		// $500 / 2400 fields = $0.2083/field -- best rate, rewards the
		// larger one-time commitment. Also a starting number.
		ID: "annual", Label: "Annual",
		PricePerUnit: 500, UnitLabel: "annual plan", Step: 1, FieldsPerUnit: 2400,
		ExpiresAfter: &oneYear,
	},
}

// ── Wiring ──────────────────────────────────────────────────────────────────

var (
	paymentGateway interfaces.PaymentGateway
	txStore        interfaces.PlanTransactionStore
)

// InitPayment wires the payment gateway and transaction store used by
// PayHandler. Call once from main.go after idb.Init(...) and after the
// store's schema has been ensured, e.g.:
//
//	store, _ := planstore.NewStore(os.Getenv("DB_DRIVER"), idb.Pool(), nil)
//	store.EnsureSchema(ctx)
//	routers.InitPayment(payment.NewGateway(os.Getenv("PAYMENT_GATEWAY")), store)
func InitPayment(gateway interfaces.PaymentGateway, store interfaces.PlanTransactionStore) {
	paymentGateway = gateway
	txStore = store
}

// ── Pricing (shared by both handlers below) ──────────────────────────────────

type paymentCalculationResponse struct {
	PlanID       string  `json:"planId"`
	PlanLabel    string  `json:"planLabel"`
	Quantity     int     `json:"quantity"`
	UnitLabel    string  `json:"unitLabel"`
	PricePerUnit float64 `json:"pricePerUnit"`
	TotalFields  int     `json:"totalFields"`
	Total        float64 `json:"total"`

	// Coupon fields. Zero/empty when no coupon was supplied, so an older
	// client that ignores them still reads Total as the amount to pay.
	// A client that DOES send a coupon must use FinalTotal -- Total stays
	// the undiscounted list price so the UI can show it struck through.
	CouponCode        string  `json:"couponCode,omitempty"`
	CouponDescription string  `json:"couponDescription,omitempty"`
	Discount          float64 `json:"discount"`
	FinalTotal        float64 `json:"finalTotal"`
	CouponError       string  `json:"couponError,omitempty"`

	// ExpiresAt is the exact date these fields would lapse on if bought
	// right now — nil for plans that never expire (free, per-field,
	// bundle). Shown on the checkout page BEFORE purchase, so "these 200
	// fields expire in 30 days" is something the user sees while
	// deciding, not something they discover afterward.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

func calculatePrice(planID string, quantity int) (*paymentCalculationResponse, error) {
	p, ok := plans[planID]
	if !ok {
		return nil, fmt.Errorf("unknown planId")
	}
	if quantity <= 0 {
		return nil, fmt.Errorf("quantity must be a positive number")
	}
	if quantity%p.Step != 0 {
		return nil, fmt.Errorf("quantity must be a multiple of %d", p.Step)
	}
	total := roundToCents(float64(quantity) * p.PricePerUnit)
	return &paymentCalculationResponse{
		PlanID:       p.ID,
		PlanLabel:    p.Label,
		Quantity:     quantity,
		UnitLabel:    p.UnitLabel,
		PricePerUnit: p.PricePerUnit,
		TotalFields:  quantity * p.FieldsPerUnit,
		Total:        total,
		FinalTotal:   total, // overwritten by applyCoupon when one applies
		ExpiresAt:    p.expiresAt(time.Now()),
	}, nil
}

// applyCoupon folds a coupon preview into a price calculation.
//
// A bad coupon never fails the calculation -- the price is still valid
// without it, and the checkout page needs to render both the price AND
// the reason the code was rejected. So the reason goes in CouponError and
// FinalTotal stays at the undiscounted total.
func applyCoupon(ctx context.Context, calc *paymentCalculationResponse, code, userID string) {
	calc.FinalTotal = calc.Total
	if code == "" || couponStore == nil {
		return
	}
	eval, err := couponStore.Evaluate(ctx, code, userID, calc.PlanID, calc.Total)
	if err != nil {
		imetrics.CouponRejections.With(icoupon.Reason(err)).Inc()
		calc.CouponError = err.Error()
		return
	}
	calc.CouponCode = eval.Code
	calc.CouponDescription = eval.Description
	calc.Discount = eval.DiscountAmount
	calc.FinalTotal = eval.FinalAmount
}

func roundToCents(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

// ── POST /api/payment/calculate — pricing preview, no charge ────────────────

type paymentCalculationRequest struct {
	PlanID     string `json:"planId"`
	Quantity   int    `json:"quantity"`
	CouponCode string `json:"couponCode"`
}

// PaymentCalculationHandler validates the requested plan + quantity and
// returns the calculated price, without charging anything. Used by
// PaymentCalculationPage while the user is still adjusting quantity.
// Wired as: r.Post("/calculate", routers.PaymentCalculationHandler) inside PaymentRouter()
func PaymentCalculationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}

	var req paymentCalculationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	calc, err := calculatePrice(req.PlanID, req.Quantity)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// /calculate is public (see PaymentRouter), so there may be no
	// identity here. Without one the per-user redemption limit cannot be
	// checked, so the preview may be optimistic -- PayHandler re-checks
	// against the real user and is the authority.
	applyCoupon(r.Context(), calc, req.CouponCode, imw.UserID(r))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(calc)
}

// ── POST /api/payment/pay — actually charge + record the transaction ───────

type payRequest struct {
	UserID     string `json:"userId"`
	Username   string `json:"username"`
	Email      string `json:"email"`
	PlanID     string `json:"planId"`
	Quantity   int    `json:"quantity"`
	Currency   string `json:"currency"`
	CouponCode string `json:"couponCode"`
}

type payResponse struct {
	TransactionID  string  `json:"transactionId"`
	Status         string  `json:"status"`
	PaymentGateway string  `json:"paymentGateway"`
	PlanID         string  `json:"planId"`
	PlanLabel      string  `json:"planLabel"`
	Quantity       int     `json:"quantity"`
	FieldsAllotted int     `json:"fieldsAllotted"`
	Total          float64 `json:"total"`
	Currency       string  `json:"currency"`

	CouponCode string  `json:"couponCode,omitempty"`
	Discount   float64 `json:"discount,omitempty"`

	// Only populated for gateways that need a client-side step to finish
	// the payment (see interfaces.ChargeResult). Frontend checks Status:
	// "succeeded" → done; "pending" → use these to complete the payment,
	// then call POST /api/payment/verify.
	ClientSecret   string `json:"clientSecret,omitempty"`
	GatewayOrderID string `json:"gatewayOrderId,omitempty"`
	GatewayKeyID   string `json:"gatewayKeyId,omitempty"`
}

// PayHandler prices the request, charges it through whichever
// interfaces.PaymentGateway was wired up in InitPayment, and records the
// outcome in plan_transactions via the configured
// interfaces.PlanTransactionStore.
//
// UserID is taken from the gateway-verified X-User-Id header (set by
// imw.Identity, mounted on this route in PaymentRouter()), never trusted
// from the request body — otherwise any caller could attribute a purchase
// to someone else's account just by putting a different userId in the
// JSON. Username/email from the body are kept only as display metadata for
// the transaction record (e.g. what to show on a receipt).
// Wired as: r.Post("/api/payment/pay", routers.PayHandler) inside PaymentRouter()
// FreeGrantFields is how many tokens the Free plan grants, once, per user.
// Sized to cover one full real document (see field_library seed data --
// even just the built-in critical/standard fields are ~10, and real
// documents commonly need 15-30), not just a handful of test fields --
// LLM cost per field is a fraction of a cent (see cost analysis this was
// raised from 6), so being generous here barely affects margin and
// materially improves whether a trial actually demonstrates the product.
const FreeGrantFields = 30

// ClaimFreeHandler grants FreeGrantFields tokens the first time a user hits
// this endpoint, and rejects every time after. This is deliberately NOT
// routed through PayHandler/paymentGateway.Charge() — Stripe's and
// Razorpay's Charge() both reject a non-positive Amount outright, so a
// "free" $0 purchase can never cleanly go through the real-gateway path
// regardless of which PAYMENT_GATEWAY is configured. A free grant isn't a
// payment, so it doesn't pretend to be one.
// Wired as: r.Post("/claim-free", routers.ClaimFreeHandler) inside PaymentRouter()
func ClaimFreeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}
	if txStore == nil {
		writeJSONError(w, http.StatusInternalServerError, "payment is not configured — call routers.InitPayment at startup")
		return
	}

	userID := imw.UserID(r)
	ctx := r.Context()

	existing, err := txStore.ListTransactionsByUser(ctx, userID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not check existing transactions")
		return
	}
	for _, t := range existing {
		if t.PlanID == "free" && t.Status == "succeeded" {
			writeJSONError(w, http.StatusConflict, "you've already claimed your free tokens")
			return
		}
	}

	tx := &interfaces.PlanTransaction{
		ID:             newID(),
		UserID:         userID,
		PlanID:         "free",
		PlanLabel:      "Free",
		Quantity:       FreeGrantFields,
		FieldsAllotted: FreeGrantFields,
		PricePerUnit:   0,
		TotalAmount:    0,
		Currency:       "USD",
		PaymentGateway: "free", // not a real gateway — see the doc comment above
		Status:         "succeeded",
	}
	if err := txStore.InsertTransaction(ctx, tx); err != nil {
		if errors.Is(err, iplanstore.ErrDuplicate) {
			// The read above is a fast path, not the actual guarantee —
			// this is what makes it safe under concurrent requests. Two
			// requests can both pass the "already claimed?" check above
			// before either commits; the database's unique index (see
			// planstore.postgresStore.EnsureSchema) is what actually
			// stops a second free grant, and this is that rejection
			// surfacing as the same friendly response the fast-path
			// check gives.
			writeJSONError(w, http.StatusConflict, "you've already claimed your free tokens")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "could not record free grant")
		return
	}

	if err := idb.GrantTokens(ctx, userID, "free", "free_claim", &tx.ID, FreeGrantFields, nil); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "recorded but could not credit tokens — contact support")
		return
	}

	balance, _ := idb.GetTokenBalance(ctx, userID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"granted":      FreeGrantFields,
		"tokenBalance": balance,
	})
}

func PayHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}
	if paymentGateway == nil || txStore == nil {
		writeJSONError(w, http.StatusInternalServerError, "payment is not configured — call routers.InitPayment at startup")
		return
	}

	userID := imw.UserID(r)
	if userID == "" {
		// Should be unreachable — imw.Identity already rejects requests
		// without this header — but fail closed rather than silently
		// falling back to a client-supplied id.
		writeJSONError(w, http.StatusUnauthorized, "missing verified identity")
		return
	}

	var req payRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.UserID = userID
	if req.Currency == "" {
		req.Currency = "USD"
	}

	calc, err := calculatePrice(req.PlanID, req.Quantity)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	ctx := r.Context()
	txID := newID()

	// ── Reserve the coupon BEFORE charging ──────────────────────────────
	//
	// Order matters. Consuming the coupon after a successful charge would
	// let two simultaneous purchases both pass the "one use left" check
	// and both receive the discount. Reserving first makes that
	// impossible, at the cost of needing a compensating release on every
	// failure path below — which is the trade worth making, because
	// over-redemption loses money silently while a stuck reservation is
	// visible and reversible.
	amountToCharge := calc.Total
	var redemption *icoupon.Redemption

	if req.CouponCode != "" {
		if couponStore == nil {
			writeJSONError(w, http.StatusInternalServerError, "coupons are not configured on this server")
			return
		}
		red, c, err := couponStore.Redeem(ctx, icoupon.RedeemParams{
			Code:          req.CouponCode,
			UserID:        userID,
			Username:      req.Username,
			Email:         req.Email,
			PlanID:        calc.PlanID,
			Amount:        calc.Total,
			Currency:      req.Currency,
			TransactionID: txID,
		})
		if err != nil {
			imetrics.CouponRedemptions.With(couponKind(c), "rejected").Inc()
			imetrics.CouponRejections.With(icoupon.Reason(err)).Inc()
			writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if c.Kind == icoupon.KindTokens {
			// A token coupon grants credits outright; it is not a
			// discount on a purchase. Give the reservation straight back
			// rather than consuming it for nothing.
			_ = couponStore.ReleaseRedemption(ctx, red.ID)
			writeJSONError(w, http.StatusUnprocessableEntity,
				"that code grants free fields rather than a discount — claim it on the Coupons page instead")
			return
		}
		redemption = red
		amountToCharge = red.FinalAmount
		calc.CouponCode = red.Code
		calc.Discount = red.DiscountAmount
		calc.FinalTotal = red.FinalAmount
	}

	// releaseCoupon undoes the reservation. Safe to call more than once.
	releaseCoupon := func(why string) {
		if redemption == nil {
			return
		}
		if err := couponStore.ReleaseRedemption(ctx, redemption.ID); err != nil {
			imetrics.LogCtx(ctx, "error", "coupon_release_failed", map[string]any{
				"redemption_id": redemption.ID, "code": redemption.Code,
				"reason": why, "error": err.Error(),
			})
			return
		}
		imetrics.LogCtx(ctx, "info", "coupon_released", map[string]any{
			"code": redemption.Code, "user_id": userID, "reason": why,
		})
		redemption = nil
	}

	// Record the attempt before charging, so a crash mid-charge still
	// leaves a "pending" row rather than nothing at all.
	pending := &interfaces.PlanTransaction{
		ID:             txID,
		UserID:         req.UserID,
		Username:       req.Username,
		Email:          req.Email,
		PlanID:         calc.PlanID,
		PlanLabel:      calc.PlanLabel,
		Quantity:       calc.Quantity,
		FieldsAllotted: calc.TotalFields,
		PricePerUnit:   calc.PricePerUnit,
		TotalAmount:    amountToCharge,
		Currency:       req.Currency,
		PaymentGateway: paymentGateway.Name(),
		Status:         "pending",
	}
	if err := txStore.InsertTransaction(ctx, pending); err != nil {
		releaseCoupon("transaction_insert_failed")
		writeJSONError(w, http.StatusInternalServerError, "could not record transaction")
		return
	}

	var (
		result     *interfaces.ChargeResult
		chargeErr  error
		status     = "failed"
		gatewayRef = ""
	)

	if amountToCharge <= 0 {
		// A coupon took the order to zero. Stripe and Razorpay both
		// reject a non-positive amount, so there is no charge to make --
		// the same reasoning as ClaimFreeHandler. Settle it directly
		// rather than sending the gateway a request it will refuse.
		status = "succeeded"
		gatewayRef = "coupon_full_discount"
		imetrics.PaymentAttempts.With("coupon", calc.PlanID, "succeeded").Inc()
	} else {
		chargeStart := time.Now()
		chargeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result, chargeErr = paymentGateway.Charge(chargeCtx, interfaces.ChargeRequest{
			UserID:   req.UserID,
			Email:    req.Email,
			PlanID:   calc.PlanID,
			Amount:   amountToCharge,
			Currency: req.Currency,
			Metadata: map[string]string{
				"transactionId": txID,
				"quantity":      fmt.Sprintf("%d", calc.Quantity),
				"couponCode":    calc.CouponCode,
			},
		})
		cancel()

		imetrics.PaymentDuration.With(paymentGateway.Name()).Observe(time.Since(chargeStart).Seconds())

		if chargeErr == nil && result != nil {
			status = result.Status
			gatewayRef = result.TransactionID
		}
		imetrics.PaymentAttempts.With(paymentGateway.Name(), calc.PlanID, status).Inc()
		if status == "succeeded" {
			imetrics.PaymentAmount.With(paymentGateway.Name(), req.Currency).Add(amountToCharge)
		}
		if status == "failed" {
			imetrics.GetWindow(imetrics.WinPaymentFails).Observe(1)
		}
	}

	if err := txStore.UpdateStatus(ctx, txID, status, gatewayRef); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "payment processed but could not update transaction record")
		return
	}

	// The charge did not go through, so the customer keeps their coupon.
	// A "pending" result is NOT a failure -- Razorpay finishes in
	// VerifyHandler, which releases the reservation itself if the
	// verification comes back unsuccessful.
	if status == "failed" {
		releaseCoupon("charge_failed")
	}

	// Top up tokens only on a synchronous success — Stripe/Razorpay's
	// "pending" outcomes get their tokens from VerifyHandler or the
	// webhook handlers instead, once the charge actually completes.
	if status == "succeeded" {
		if err := idb.GrantTokens(ctx, userID, calc.PlanID, "purchase", &txID, calc.TotalFields, calc.ExpiresAt); err != nil {
			// Payment already succeeded and is recorded — don't fail the
			// response over this, but it needs to be visible somewhere.
			log.Printf("payment: charge %s succeeded but token top-up failed for user %s: %v", txID, userID, err)
			imetrics.LogCtx(ctx, "error", "token_topup_failed", map[string]any{
				"transaction_id": txID, "user_id": userID, "fields": calc.TotalFields,
			})
		} else {
			imetrics.TokensGranted.With("purchase").Add(float64(calc.TotalFields))
		}
		if redemption != nil {
			imetrics.CouponRedemptions.With("discount", "accepted").Inc()
			imetrics.CouponDiscount.With(req.Currency).Add(redemption.DiscountAmount)
			imetrics.GetWindow(imetrics.WinCouponRedeems).Observe(1)
		}
	}

	if chargeErr != nil {
		// chargeErr can be a raw error from the Stripe/Razorpay SDK —
		// including infrastructure-level text like "STRIPE_SECRET_KEY
		// is not set" if the gateway is misconfigured, not just card
		// declines. None of that is something a paying customer should
		// see. The real error is logged (with this request's trace id)
		// for you to look up; they get a fixed, generic message.
		writeInternalError(w, r, http.StatusPaymentRequired, chargeErr,
			"payment could not be completed — please check your payment details or try again")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(payResponse{
		TransactionID:  txID,
		Status:         status,
		PaymentGateway: paymentGateway.Name(),
		PlanID:         calc.PlanID,
		PlanLabel:      calc.PlanLabel,
		Quantity:       calc.Quantity,
		FieldsAllotted: calc.TotalFields,
		Total:          amountToCharge,
		Currency:       req.Currency,
		CouponCode:     calc.CouponCode,
		Discount:       calc.Discount,
		ClientSecret:   safeClientSecret(result),
		GatewayOrderID: safeOrderID(result),
		GatewayKeyID:   safeKeyID(result),
	})
}

// couponKind safely reads a kind for a metric label when the coupon may
// be nil (rejected before it was ever loaded).
func couponKind(c *icoupon.Coupon) string {
	if c == nil {
		return "unknown"
	}
	return c.Kind
}

func safeClientSecret(r *interfaces.ChargeResult) string {
	if r == nil {
		return ""
	}
	return r.ClientSecret
}

func safeOrderID(r *interfaces.ChargeResult) string {
	if r == nil {
		return ""
	}
	return r.GatewayOrderID
}

func safeKeyID(r *interfaces.ChargeResult) string {
	if r == nil {
		return ""
	}
	return r.GatewayKeyID
}

// ── POST /api/payment/verify — completes gateways whose Charge() left the
// transaction "pending" (Razorpay today) ───────────────────────────────────

type verifyRequest struct {
	TransactionID string            `json:"transactionId"`
	Fields        map[string]string `json:"fields"`
}

type verifyResponse struct {
	TransactionID string `json:"transactionId"`
	Status        string `json:"status"`
}

// VerifyHandler is called by the frontend once a client-side payment step
// finishes (e.g. Razorpay Checkout closing successfully). It only works for
// gateways that implement interfaces.Verifiable — for gateways that always
// resolve synchronously (mock, or Stripe once confirmed) there's nothing to
// verify here.
// Wired as: r.Post("/api/payment/verify", routers.VerifyHandler)
func VerifyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}
	if paymentGateway == nil || txStore == nil {
		writeJSONError(w, http.StatusInternalServerError, "payment is not configured — call routers.InitPayment at startup")
		return
	}

	verifiable, ok := paymentGateway.(interfaces.Verifiable)
	if !ok {
		writeJSONError(w, http.StatusNotImplemented, fmt.Sprintf("%s does not require a separate verification step", paymentGateway.Name()))
		return
	}

	var req verifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TransactionID == "" {
		writeJSONError(w, http.StatusBadRequest, "transactionId is required")
		return
	}

	ctx := r.Context()

	// Ownership check — a user can only verify/complete their own
	// transaction, not one they guessed the id of.
	userID := imw.UserID(r)
	existing, err := txStore.GetTransaction(ctx, req.TransactionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not look up transaction")
		return
	}
	if existing == nil {
		writeJSONError(w, http.StatusNotFound, "transaction not found")
		return
	}
	if existing.UserID != userID {
		writeJSONError(w, http.StatusForbidden, "transaction does not belong to this user")
		return
	}
	// A refunded payment must not be re-completable. Razorpay's payment
	// signature stays valid forever, so without this a refunded customer
	// could replay their own /verify call: it would flip the row back to
	// "succeeded" and grant the credits a second time -- keeping both the
	// refund and the credits.
	if isRefundedStatus(existing.Status) {
		writeJSONError(w, http.StatusConflict, "this payment has been refunded")
		return
	}

	result, err := verifiable.Verify(ctx, interfaces.VerifyRequest{
		TransactionID: req.TransactionID,
		Fields:        req.Fields,
	})
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, "verification failed: "+err.Error())
		return
	}

	// Guard against double-crediting: if the webhook already flipped this
	// transaction to "succeeded" (e.g. it beat this request to the punch),
	// don't add tokens a second time here.
	alreadyCredited := existing.Status == "succeeded"

	if err := txStore.UpdateStatus(ctx, req.TransactionID, result.Status, result.TransactionID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "verified but could not update transaction record")
		return
	}

	if result.Status != "succeeded" {
		// The client-side step did not complete, so any coupon reserved
		// by PayHandler for this transaction goes back to the customer.
		// Without this, an abandoned Razorpay checkout would quietly
		// consume a single-use code.
		releaseCouponForTransaction(ctx, req.TransactionID, "verification_failed")
		writeJSONError(w, http.StatusPaymentRequired, "payment verification did not succeed")
		return
	}

	if !alreadyCredited {
		// Anchored to when the purchase was originally made
		// (existing.CreatedAt), not to now — verification can settle
		// some time after the charge, and "expires 30 days from
		// purchase" should mean the purchase, not whenever this
		// request happened to land.
		expiresAt := plans[existing.PlanID].expiresAt(existing.CreatedAt)
		if err := idb.GrantTokens(ctx, existing.UserID, existing.PlanID, "purchase", &existing.ID, existing.FieldsAllotted, expiresAt); err != nil {
			log.Printf("payment: verify succeeded for %s but token top-up failed: %v", req.TransactionID, err)
		} else {
			imetrics.TokensGranted.With("purchase").Add(float64(existing.FieldsAllotted))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(verifyResponse{
		TransactionID: req.TransactionID,
		Status:        result.Status,
	})
}

// ── Router ────────────────────────────────────────────────────────────────

// PaymentRouter mounts all payment endpoints under /api/payment.
//
//   - /calculate         — public, no charge, price preview only
//   - /webhook/stripe     — public, verified via Stripe-Signature instead of JWT
//   - /webhook/razorpay   — public, verified via X-Razorpay-Signature instead of JWT
//   - /pay, /verify       — require a verified user identity (imw.Identity),
//     which reads the X-User-Id header the gateway injects after checking
//     the caller's JWT. Direct :8000 access without going through the
//     gateway has no such header and is rejected.
//
// Wired as: r.Mount("/api/payment", routers.PaymentRouter())
// The gateway's own publicPaths/publicPrefixes (hitl-go/gateway/main.go)
// must also list /api/payment/calculate and /api/payment/webhook/ as
// public, or requests never reach here — the gateway rejects unrecognized
// non-public paths before proxying.
func PaymentRouter() http.Handler {
	r := chi.NewRouter()

	r.Post("/calculate", PaymentCalculationHandler)
	r.Post("/webhook/stripe", StripeWebhookHandler)
	r.Post("/webhook/razorpay", RazorpayWebhookHandler)

	r.Group(func(r chi.Router) {
		r.Use(imw.Identity)
		r.Post("/pay", PayHandler)
		r.Post("/verify", VerifyHandler)
		r.Post("/claim-free", ClaimFreeHandler)
	})

	return r
}

// releaseCouponForTransaction gives back whatever coupon was reserved for
// a transaction that ultimately did not complete.
//
// Best-effort by design: it is called on failure paths that are already
// returning an error to the user, and a release that fails should not
// turn "your payment did not go through" into a 500. The failure is
// logged with the redemption id so it can be released by hand, and the
// coupon_released / coupon_release_failed log pair makes the rate of this
// visible.
func releaseCouponForTransaction(ctx context.Context, transactionID, why string) {
	if couponStore == nil || transactionID == "" {
		return
	}
	id, err := couponStore.RedemptionIDForTransaction(ctx, transactionID)
	if err != nil || id == "" {
		return
	}
	if err := couponStore.ReleaseRedemption(ctx, id); err != nil {
		imetrics.LogCtx(ctx, "error", "coupon_release_failed", map[string]any{
			"redemption_id": id, "transaction_id": transactionID,
			"reason": why, "error": err.Error(),
		})
		return
	}
	imetrics.LogCtx(ctx, "info", "coupon_released", map[string]any{
		"redemption_id": id, "transaction_id": transactionID, "reason": why,
	})
}
