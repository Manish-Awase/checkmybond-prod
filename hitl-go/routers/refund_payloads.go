// routers/refund_payloads.go
//
// The parts of the Stripe/Razorpay webhook handling that are pure data
// handling -- payload shapes, the refund arithmetic, the credited-status
// check -- kept apart from the handlers in payment_webhooks.go so they
// depend on nothing but the standard library and can be unit-tested
// directly (refund_payloads_test.go).
package routers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ── Notes ────────────────────────────────────────────────────────────────────

// gatewayNotes is a string map that tolerates what Razorpay actually
// sends for empty notes: an empty JSON ARRAY ("notes": []), not an empty
// object. A plain map[string]string fails to unmarshal that with a type
// error, which would make the whole webhook body fail to parse (a 400 to
// Razorpay, which then retries and eventually flags the endpoint as
// failing) -- and refunds with no comment typed are exactly the case
// that sends it. Razorpay's own refund docs show `"notes": []` in a
// sample payload.
type gatewayNotes map[string]string

func (n *gatewayNotes) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		// [] (Razorpay's "no notes"), null, or anything else that isn't
		// an object: no usable notes, and not an error.
		*n = nil
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return err
	}
	out := make(gatewayNotes, len(raw))
	for k, v := range raw {
		switch t := v.(type) {
		case string:
			out[k] = t
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprint(t)
		}
	}
	*n = out
	return nil
}

// ── Razorpay ─────────────────────────────────────────────────────────────────

type razorpayPaymentEntity struct {
	ID       string `json:"id"`
	Amount   int64  `json:"amount"` // paise (smallest unit)
	Currency string `json:"currency"`
	// Cumulative across every refund on this payment, which is what makes
	// recording it idempotent -- refund.created and refund.processed both
	// fire for the same refund and must not double-count.
	AmountRefunded int64        `json:"amount_refunded"`
	RefundStatus   string       `json:"refund_status"` // "partial" | "full" | "" (null)
	Notes          gatewayNotes `json:"notes"`
}

type razorpayRefundEntity struct {
	ID        string `json:"id"`
	PaymentID string `json:"payment_id"`
	Amount    int64  `json:"amount"` // this one refund only, not cumulative
	Currency  string `json:"currency"`
	Status    string `json:"status"`
	// Free text typed by whoever issued the refund -- NOT our
	// transactionId. That lives on the payment's notes (or is found via
	// payment_id); see the refund case in RazorpayWebhookHandler.
	Notes gatewayNotes `json:"notes"`
}

type razorpayWebhookPayload struct {
	Event   string `json:"event"`
	Payload struct {
		Payment struct {
			Entity razorpayPaymentEntity `json:"entity"`
		} `json:"payment"`
		Refund struct {
			Entity razorpayRefundEntity `json:"entity"`
		} `json:"refund"`
	} `json:"payload"`
}

// ── Stripe ───────────────────────────────────────────────────────────────────

// stripeChargePayload is the slice of a Stripe Charge (the object inside
// a charge.refunded event) that refund recording needs. Stripe copies a
// PaymentIntent's metadata onto the charge it creates, which is why our
// transactionId is on it -- but PaymentIntent is also kept as the
// fallback lookup key.
type stripeChargePayload struct {
	ID             string            `json:"id"`
	PaymentIntent  string            `json:"payment_intent"`
	Amount         int64             `json:"amount"`
	AmountRefunded int64             `json:"amount_refunded"`
	Refunded       bool              `json:"refunded"` // true ONLY for a full refund; stays false on a partial one
	Currency       string            `json:"currency"`
	Metadata       map[string]string `json:"metadata"`
}

// ── Refund arithmetic ────────────────────────────────────────────────────────

// refundState is a gateway's statement of how much of a payment has been
// refunded so far, in major units (dollars, not cents).
type refundState struct {
	Amount float64
	Full   bool
}

var (
	zeroDecimalCurrencies = map[string]bool{
		"bif": true, "clp": true, "djf": true, "gnf": true, "jpy": true, "kmf": true, "krw": true,
		"mga": true, "pyg": true, "rwf": true, "ugx": true, "vnd": true, "vuv": true, "xaf": true,
		"xof": true, "xpf": true,
	}
	threeDecimalCurrencies = map[string]bool{
		"bhd": true, "iqd": true, "jod": true, "kwd": true, "lyd": true, "omr": true, "tnd": true,
	}
)

// minorToMajor converts a gateway amount in the smallest currency unit
// (cents, paise) to the major unit plan_transactions stores. Both
// gateways report minor units; plan_transactions.total_amount is major.
func minorToMajor(amount int64, currency string) float64 {
	c := strings.ToLower(currency)
	switch {
	case zeroDecimalCurrencies[c]:
		return float64(amount)
	case threeDecimalCurrencies[c]:
		return float64(amount) / 1000
	default:
		return float64(amount) / 100
	}
}

// stripeRefundState reads the refund position off a charge.refunded
// event. That event fires for partial refunds as well as full ones, and
// `refunded` stays false on a partial one -- so "full" is decided by
// comparing amounts, with the boolean as a second signal, never by the
// boolean alone.
func stripeRefundState(c stripeChargePayload) refundState {
	full := c.Refunded || (c.Amount > 0 && c.AmountRefunded >= c.Amount)
	return refundState{Amount: minorToMajor(c.AmountRefunded, c.Currency), Full: full}
}

// razorpayRefundState reads the refund position off a refund.* event.
// The payment entity carries the cumulative total, so it is preferred;
// the single refund's own amount is only a fallback for a payload that
// somehow lacks (or lags behind on) the payment entity.
func razorpayRefundState(pay razorpayPaymentEntity, ref razorpayRefundEntity) refundState {
	refunded := pay.AmountRefunded
	if refunded == 0 {
		refunded = ref.Amount
	}
	currency := pay.Currency
	if currency == "" {
		currency = ref.Currency
	}
	full := pay.RefundStatus == "full" || (pay.Amount > 0 && refunded >= pay.Amount)
	return refundState{Amount: minorToMajor(refunded, currency), Full: full}
}

// isCreditedStatus reports whether a transaction has already passed
// through "succeeded" -- i.e. its credits were granted and it must never
// be moved back to "succeeded" or granted again. A payment_intent.
// succeeded webhook retry arriving AFTER a refund was recorded would
// otherwise flip a refunded row back to succeeded and grant the credits
// a second time; gateways don't guarantee event ordering.
func isCreditedStatus(status string) bool {
	return status == "succeeded" || status == "partially_refunded" || status == "refunded"
}

// isRefundedStatus reports whether a transaction has been (fully or
// partially) refunded. Once it has, no purchase-flow event -- a late or
// replayed gateway webhook, or a client re-calling /verify with its
// still-valid payment signature -- may change its status or grant its
// credits again.
func isRefundedStatus(status string) bool {
	return status == "partially_refunded" || status == "refunded"
}
