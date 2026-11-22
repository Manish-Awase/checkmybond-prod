package routers

import (
	"encoding/json"
	"math"
	"testing"
)

// Razorpay's published sample for refund.created
// (https://razorpay.com/docs/webhooks/refunds), trimmed to the fields that
// matter but keeping its two quirks: the refund's notes are a free-text
// object, and the PAYMENT's notes are an empty ARRAY.
const razorpaySampleRefundCreated = `{
  "entity": "event",
  "event": "refund.created",
  "contains": ["refund", "payment"],
  "payload": {
    "refund": {"entity": {
      "id": "rfnd_FS8TWyPrCsa0OB", "entity": "refund", "amount": 50000, "currency": "USD",
      "payment_id": "pay_FPoJKWQQ8lK13n",
      "notes": {"comment": "Customer Notes for Webhooks."},
      "status": "processed"}},
    "payment": {"entity": {
      "id": "pay_FPoJKWQQ8lK13n", "entity": "payment", "amount": 500000, "currency": "USD",
      "status": "captured", "amount_refunded": 190000, "refund_status": "partial",
      "notes": []}}
  }
}`

func TestRazorpaySamplePayloadParses(t *testing.T) {
	var evt razorpayWebhookPayload
	if err := json.Unmarshal([]byte(razorpaySampleRefundCreated), &evt); err != nil {
		t.Fatalf("Razorpay's own sample payload must parse (empty notes are []): %v", err)
	}
	pay, ref := evt.Payload.Payment.Entity, evt.Payload.Refund.Entity
	if pay.ID != "pay_FPoJKWQQ8lK13n" || ref.PaymentID != pay.ID {
		t.Errorf("ids not read: pay=%q ref.payment_id=%q", pay.ID, ref.PaymentID)
	}
	if got := pay.Notes["transactionId"]; got != "" {
		t.Errorf("empty notes array should yield no keys, got %q", got)
	}
	if ref.Notes["comment"] == "" {
		t.Error("object notes should still parse")
	}
	// The refund's notes are what the person issuing it typed. They must
	// never be treated as our transactionId.
	if ref.Notes["transactionId"] != "" {
		t.Error("refund notes unexpectedly contain transactionId")
	}
}

func TestRazorpayRefundStateUsesCumulativePaymentAmount(t *testing.T) {
	var evt razorpayWebhookPayload
	_ = json.Unmarshal([]byte(razorpaySampleRefundCreated), &evt)
	st := razorpayRefundState(evt.Payload.Payment.Entity, evt.Payload.Refund.Entity)
	// 190000 paise/cents cumulative -- NOT this single refund's 50000.
	if st.Amount != 1900.00 {
		t.Errorf("want cumulative 1900.00, got %v", st.Amount)
	}
	if st.Full {
		t.Error("190000 of 500000 must not be reported as a full refund")
	}
}

func TestRazorpayRefundStateFull(t *testing.T) {
	pay := razorpayPaymentEntity{Amount: 500000, AmountRefunded: 500000, RefundStatus: "full", Currency: "INR"}
	if st := razorpayRefundState(pay, razorpayRefundEntity{Amount: 500000}); !st.Full || st.Amount != 5000 {
		t.Errorf("full refund: %+v", st)
	}
	// Full detected from amounts alone when refund_status is missing.
	pay.RefundStatus = ""
	if st := razorpayRefundState(pay, razorpayRefundEntity{}); !st.Full {
		t.Error("amount_refunded >= amount should count as full without refund_status")
	}
}

func TestRazorpayRefundStateFallsBackToRefundAmount(t *testing.T) {
	// No payment entity at all: fall back to the refund's own amount.
	st := razorpayRefundState(razorpayPaymentEntity{}, razorpayRefundEntity{Amount: 2500, Currency: "INR"})
	if st.Amount != 25.00 || st.Full {
		t.Errorf("fallback: %+v", st)
	}
}

func TestGatewayNotesTolerance(t *testing.T) {
	cases := map[string]string{
		`[]`:                      "",
		`null`:                    "",
		`{}`:                      "",
		`{"transactionId":"abc"}`: "abc",
		`{"transactionId":123}`:   "123",
		`{"transactionId":null}`:  "",
		`["not","an","object"]`:   "",
	}
	for in, want := range cases {
		var n gatewayNotes
		if err := json.Unmarshal([]byte(in), &n); err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got := n["transactionId"]; got != want {
			t.Errorf("%s: want %q, got %q", in, want, got)
		}
	}
	// Reading a key from a nil map is fine -- the handlers rely on it.
	var nilNotes gatewayNotes
	if nilNotes["x"] != "" {
		t.Error("nil map lookup")
	}
}

func TestStripePartialRefundIsNotFull(t *testing.T) {
	// Stripe: `refunded` stays false on a partial refund.
	st := stripeRefundState(stripeChargePayload{Amount: 5000, AmountRefunded: 2000, Refunded: false, Currency: "usd"})
	if st.Full || st.Amount != 20.00 {
		t.Errorf("partial: %+v", st)
	}
}

func TestStripeFullRefund(t *testing.T) {
	st := stripeRefundState(stripeChargePayload{Amount: 5000, AmountRefunded: 5000, Refunded: true, Currency: "usd"})
	if !st.Full || st.Amount != 50.00 {
		t.Errorf("full: %+v", st)
	}
	// Full even if the boolean were somehow absent.
	st = stripeRefundState(stripeChargePayload{Amount: 5000, AmountRefunded: 5000, Currency: "usd"})
	if !st.Full {
		t.Error("amounts equal should count as full")
	}
}

func TestStripeChargePayloadParses(t *testing.T) {
	raw := `{"id":"ch_1","payment_intent":"pi_1","amount":5000,"amount_refunded":2000,
	         "refunded":false,"currency":"usd","metadata":{"transactionId":"tx_9"}}`
	var c stripeChargePayload
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if c.PaymentIntent != "pi_1" || c.Metadata["transactionId"] != "tx_9" || c.AmountRefunded != 2000 {
		t.Errorf("%+v", c)
	}
	// A charge with no payment intent (null) must still parse.
	if err := json.Unmarshal([]byte(`{"id":"ch_2","payment_intent":null,"amount":100,"metadata":{}}`), &c); err != nil {
		t.Errorf("null payment_intent: %v", err)
	}
}

func TestMinorToMajor(t *testing.T) {
	cases := []struct {
		amt  int64
		cur  string
		want float64
	}{
		{5000, "usd", 50}, {5000, "USD", 50}, {12345, "inr", 123.45},
		{500, "jpy", 500}, {5000, "kwd", 5},
	}
	for _, c := range cases {
		if got := minorToMajor(c.amt, c.cur); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("minorToMajor(%d,%s)=%v want %v", c.amt, c.cur, got, c.want)
		}
	}
}

func TestIsCreditedStatus(t *testing.T) {
	for s, want := range map[string]bool{
		"succeeded": true, "partially_refunded": true, "refunded": true,
		"pending": false, "failed": false, "": false,
	} {
		if got := isCreditedStatus(s); got != want {
			t.Errorf("isCreditedStatus(%q)=%v want %v", s, got, want)
		}
	}
}

func TestIsRefundedStatus(t *testing.T) {
	for s, want := range map[string]bool{
		"refunded": true, "partially_refunded": true,
		"succeeded": false, "pending": false, "failed": false, "": false,
	} {
		if got := isRefundedStatus(s); got != want {
			t.Errorf("isRefundedStatus(%q)=%v want %v", s, got, want)
		}
	}
}
