// routers/payment_webhooks.go
//
// Handles asynchronous events from Stripe and Razorpay — the things that
// happen *after* PayHandler/VerifyHandler already returned: a delayed
// PaymentIntent confirmation, a refund, a dispute. Without these,
// plan_transactions can drift out of sync with what actually happened at
// the gateway (e.g. a refund on Stripe's dashboard would never be
// reflected here).
//
// Both handlers are deliberately NOT behind imw.Identity — Stripe and
// Razorpay don't have a user JWT to send. Trust here comes entirely from
// verifying each request's signature against a webhook-specific secret
// (STRIPE_WEBHOOK_SECRET / RAZORPAY_WEBHOOK_SECRET), which is why both
// handlers read the raw body BEFORE doing anything else — signature
// verification fails if the body has been touched in any way (re-parsed
// and re-marshaled JSON will not match).
package routers

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"

	idb "hitl-platform/internal/db"
	"hitl-platform/internal/interfaces"
	"hitl-platform/internal/payment"
)

const maxWebhookBodyBytes = 64 * 1024 // Stripe/Razorpay payloads are small; cap defensively

// ── Stripe ───────────────────────────────────────────────────────────────

// Minimal shape of the PaymentIntent object Stripe embeds in
// payment_intent.* events — just the fields this handler needs.
type stripePaymentIntentPayload struct {
	ID       string            `json:"id"`
	Metadata map[string]string `json:"metadata"`
}

// StripeWebhookHandler handles payment_intent.succeeded,
// payment_intent.payment_failed, charge.refunded, and
// charge.dispute.created. Configure this URL
// (https://<your-domain>/api/payment/webhook/stripe) in the Stripe
// Dashboard → Developers → Webhooks, and set STRIPE_WEBHOOK_SECRET to the
// signing secret it gives you for that endpoint.
// Wired as: r.Post("/webhook/stripe", routers.StripeWebhookHandler) inside PaymentRouter()
func StripeWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}
	if txStore == nil {
		writeJSONError(w, http.StatusInternalServerError, "payment is not configured — call routers.InitPayment at startup")
		return
	}

	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "could not read request body")
		return
	}

	event, err := payment.ConstructWebhookEvent(payload, r.Header.Get("Stripe-Signature"))
	if err != nil {
		// Do not leak verification details — just refuse.
		writeJSONError(w, http.StatusBadRequest, "signature verification failed")
		return
	}

	ctx := r.Context()

	switch event.Type {
	case "payment_intent.succeeded", "payment_intent.payment_failed", "payment_intent.canceled":
		var pi stripePaymentIntentPayload
		if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
			log.Printf("stripe webhook: could not parse payment_intent payload: %v", err)
			writeJSONError(w, http.StatusBadRequest, "malformed event payload")
			return
		}
		txID := pi.Metadata["transactionId"]
		if txID == "" {
			// Not one of ours (or created outside this app) — Stripe still
			// expects a 2xx or it will keep retrying forever.
			log.Printf("stripe webhook: %s has no transactionId metadata, ignoring", pi.ID)
			w.WriteHeader(http.StatusOK)
			return
		}

		status := "pending"
		switch event.Type {
		case "payment_intent.succeeded":
			status = "succeeded"
		case "payment_intent.payment_failed", "payment_intent.canceled":
			status = "failed"
		}

		// Fetch current state first — PayHandler may have already credited
		// this exact PaymentIntent synchronously (when a payment method was
		// provided up front) moments before this webhook arrives for the
		// same event. Only credit tokens on a genuine pending→succeeded
		// transition, never twice for the same transaction.
		existing, getErr := txStore.GetTransaction(ctx, txID)
		if getErr != nil || existing == nil {
			log.Printf("stripe webhook: could not look up transaction %s: %v", txID, getErr)
			writeJSONError(w, http.StatusInternalServerError, "could not look up transaction")
			return
		}
		// Gateways don't guarantee event order: a retried
		// payment_intent.succeeded can land AFTER charge.refunded was
		// recorded. Without this it would flip the row back to
		// "succeeded" and grant the credits a second time.
		if isRefundedStatus(existing.Status) {
			log.Printf("stripe webhook: ignoring %s for transaction %s -- already %s", event.Type, txID, existing.Status)
			w.WriteHeader(http.StatusOK)
			return
		}
		// isCreditedStatus rather than a bare == "succeeded" check: the
		// refund guard above has already returned early for a refunded
		// row, but routing through the same helper keeps this in sync
		// with that definition instead of silently drifting from it.
		alreadyCredited := isCreditedStatus(existing.Status)

		if err := txStore.UpdateStatus(ctx, txID, status, pi.ID); err != nil {
			log.Printf("stripe webhook: failed to update transaction %s: %v", txID, err)
			writeJSONError(w, http.StatusInternalServerError, "could not update transaction")
			return
		}

		if status == "succeeded" && !alreadyCredited {
			expiresAt := plans[existing.PlanID].expiresAt(existing.CreatedAt)
			if err := idb.GrantTokens(ctx, existing.UserID, existing.PlanID, "purchase", &existing.ID, existing.FieldsAllotted, expiresAt); err != nil {
				log.Printf("stripe webhook: transaction %s succeeded but token top-up failed: %v", txID, err)
			}
		}

	case "charge.refunded":
		// Fires for PARTIAL refunds as well as full ones (the charge's
		// `refunded` flag only turns true when everything is refunded), so
		// what gets recorded is the amount, not just "refunded".
		var charge stripeChargePayload
		if err := json.Unmarshal(event.Data.Raw, &charge); err != nil {
			log.Printf("stripe webhook: could not parse charge payload: %v", err)
			writeJSONError(w, http.StatusBadRequest, "malformed event payload")
			return
		}
		// Our transactionId is on the charge's metadata (Stripe copies a
		// PaymentIntent's metadata onto its charge); the PaymentIntent id
		// is the fallback, and works for a refund issued from the Stripe
		// Dashboard just as well.
		existing, err := resolveRefundTransaction(ctx, charge.Metadata["transactionId"], charge.PaymentIntent)
		if err != nil {
			// 5xx, not 2xx: a failure here must make Stripe retry, or the
			// refund is silently never recorded.
			log.Printf("stripe webhook: could not look up transaction for charge %s: %v", charge.ID, err)
			writeJSONError(w, http.StatusInternalServerError, "could not look up transaction")
			return
		}
		if existing == nil {
			log.Printf("stripe webhook: refund on charge %s (payment_intent %s) matches no transaction in this app -- not recorded", charge.ID, charge.PaymentIntent)
			w.WriteHeader(http.StatusOK) // retrying can't fix this
			return
		}
		st := stripeRefundState(charge)
		if err := txStore.RecordRefund(ctx, existing.ID, st.Amount, st.Full); err != nil {
			log.Printf("stripe webhook: failed to record refund on transaction %s: %v", existing.ID, err)
			writeJSONError(w, http.StatusInternalServerError, "could not record refund")
			return
		}
		log.Printf("stripe webhook: recorded refund of %.2f (full=%v) on transaction %s", st.Amount, st.Full, existing.ID)

	case "charge.dispute.created":
		var dispute struct {
			PaymentIntent string `json:"payment_intent"`
		}
		if err := json.Unmarshal(event.Data.Raw, &dispute); err != nil {
			log.Printf("stripe webhook: could not parse dispute payload: %v", err)
		}
		// We don't have transactionId on the dispute object itself (only
		// on the underlying PaymentIntent's metadata, which Stripe doesn't
		// echo into dispute events) — log for manual follow-up rather than
		// guessing which row to touch.
		log.Printf("stripe webhook: dispute opened on payment_intent %s — needs manual review", dispute.PaymentIntent)

	default:
		// Unhandled event type — fine, Stripe sends many we don't care
		// about. Acknowledge so it doesn't retry.
	}

	w.WriteHeader(http.StatusOK)
}

// ── Razorpay ─────────────────────────────────────────────────────────────

// RazorpayWebhookHandler handles payment.captured, payment.failed, and
// refund.created / refund.processed. Configure this URL
// (https://<your-domain>/api/payment/webhook/razorpay) in Razorpay
// Dashboard → Settings → Webhooks, and set RAZORPAY_WEBHOOK_SECRET to the
// secret you choose there (this is separate from RAZORPAY_KEY_SECRET).
// Wired as: r.Post("/webhook/razorpay", routers.RazorpayWebhookHandler) inside PaymentRouter()
func RazorpayWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}
	if txStore == nil {
		writeJSONError(w, http.StatusInternalServerError, "payment is not configured — call routers.InitPayment at startup")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "could not read request body")
		return
	}

	if !payment.VerifyWebhookSignature(body, r.Header.Get("X-Razorpay-Signature")) {
		writeJSONError(w, http.StatusBadRequest, "signature verification failed")
		return
	}

	var evt razorpayWebhookPayload
	if err := json.Unmarshal(body, &evt); err != nil {
		writeJSONError(w, http.StatusBadRequest, "malformed event payload")
		return
	}

	ctx := r.Context()

	switch evt.Event {
	case "payment.captured", "payment.failed":
		txID := evt.Payload.Payment.Entity.Notes["transactionId"]
		if txID == "" {
			log.Printf("razorpay webhook: payment %s has no transactionId note, ignoring", evt.Payload.Payment.Entity.ID)
			w.WriteHeader(http.StatusOK)
			return
		}
		status := "succeeded"
		if evt.Event == "payment.failed" {
			status = "failed"
		}

		// Same idempotency concern as Stripe's webhook above — VerifyHandler
		// (the frontend's Checkout confirmation call) may have already
		// credited this transaction before this webhook arrives.
		existing, getErr := txStore.GetTransaction(ctx, txID)
		if getErr != nil || existing == nil {
			log.Printf("razorpay webhook: could not look up transaction %s: %v", txID, getErr)
			writeJSONError(w, http.StatusInternalServerError, "could not look up transaction")
			return
		}
		if isRefundedStatus(existing.Status) {
			log.Printf("razorpay webhook: ignoring %s for transaction %s -- already %s", evt.Event, txID, existing.Status)
			w.WriteHeader(http.StatusOK)
			return
		}
		// isCreditedStatus rather than a bare == "succeeded" check: the
		// refund guard above has already returned early for a refunded
		// row, but routing through the same helper keeps this in sync
		// with that definition instead of silently drifting from it.
		alreadyCredited := isCreditedStatus(existing.Status)

		if err := txStore.UpdateStatus(ctx, txID, status, evt.Payload.Payment.Entity.ID); err != nil {
			log.Printf("razorpay webhook: failed to update transaction %s: %v", txID, err)
			writeJSONError(w, http.StatusInternalServerError, "could not update transaction")
			return
		}

		if status == "succeeded" && !alreadyCredited {
			expiresAt := plans[existing.PlanID].expiresAt(existing.CreatedAt)
			if err := idb.GrantTokens(ctx, existing.UserID, existing.PlanID, "purchase", &existing.ID, existing.FieldsAllotted, expiresAt); err != nil {
				log.Printf("razorpay webhook: transaction %s succeeded but token top-up failed: %v", txID, err)
			}
		}

	case "refund.created", "refund.processed":
		// refund.created and refund.processed both fire for one refund,
		// and one payment can have several refunds -- so this records the
		// payment's CUMULATIVE refunded amount (a set, not an add), which
		// makes every repeat harmless.
		pay, ref := evt.Payload.Payment.Entity, evt.Payload.Refund.Entity
		// NOT ref.Notes: those are free text typed by whoever issued the
		// refund (a refund started in the Razorpay dashboard has none of
		// ours). The transactionId is on the payment's notes; the payment
		// id is the fallback.
		gatewayPaymentID := ref.PaymentID
		if gatewayPaymentID == "" {
			gatewayPaymentID = pay.ID
		}
		existing, err := resolveRefundTransaction(ctx, pay.Notes["transactionId"], gatewayPaymentID)
		if err != nil {
			log.Printf("razorpay webhook: could not look up transaction for refund %s: %v", ref.ID, err)
			writeJSONError(w, http.StatusInternalServerError, "could not look up transaction")
			return
		}
		if existing == nil {
			log.Printf("razorpay webhook: refund %s on payment %s matches no transaction in this app -- not recorded", ref.ID, gatewayPaymentID)
			w.WriteHeader(http.StatusOK) // retrying can't fix this
			return
		}
		st := razorpayRefundState(pay, ref)
		if err := txStore.RecordRefund(ctx, existing.ID, st.Amount, st.Full); err != nil {
			log.Printf("razorpay webhook: failed to record refund %s on transaction %s: %v", ref.ID, existing.ID, err)
			writeJSONError(w, http.StatusInternalServerError, "could not record refund")
			return
		}
		log.Printf("razorpay webhook: recorded refund of %.2f (full=%v) on transaction %s", st.Amount, st.Full, existing.ID)

	case "refund.failed":
		// Deliberately NOT auto-reverted: the recorded amount only moves
		// forward (see RecordRefund), and guessing what a failed refund
		// leaves behind is worse than a human checking. Loud log instead;
		// the runbook has the SQL to correct a row by hand.
		log.Printf("razorpay webhook: REFUND FAILED at the gateway (refund %s, payment %s) -- transaction stays marked refunded; check the Razorpay dashboard and correct it by hand (REFUNDS_AND_EXPIRY_RUNBOOK.md)",
			evt.Payload.Refund.Entity.ID, evt.Payload.Refund.Entity.PaymentID)

	default:
		// Unhandled event type — acknowledge so Razorpay doesn't retry.
	}

	w.WriteHeader(http.StatusOK)
}

// resolveRefundTransaction finds the plan_transactions row a refund event
// refers to. It tries our own transactionId first (present when the
// gateway echoes it back), then the gateway's payment id -- which is what
// a refund issued from the gateway's own dashboard reliably has.
// Returns (nil, nil) when neither matches: a refund on a payment this
// app never recorded.
func resolveRefundTransaction(ctx context.Context, transactionID, gatewayPaymentID string) (*interfaces.PlanTransaction, error) {
	if transactionID != "" {
		t, err := txStore.GetTransaction(ctx, transactionID)
		if err != nil {
			return nil, err
		}
		if t != nil {
			return t, nil
		}
	}
	return txStore.GetByGatewayTransactionID(ctx, gatewayPaymentID)
}
