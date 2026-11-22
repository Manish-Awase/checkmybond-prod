// internal/payment/razorpay.go
package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	razorpay "github.com/razorpay/razorpay-go"

	"hitl-platform/internal/interfaces"
)

// RazorpayGateway implements interfaces.PaymentGateway AND
// interfaces.Verifiable. Razorpay (like most Indian gateways) has no
// server-only "charge this card" API — a payment always completes via its
// Checkout widget in the browser. So the flow is two calls, not one:
//
//  1. Charge() creates a Razorpay Order and returns it "pending" along with
//     the order id + public key id. The frontend opens Razorpay Checkout
//     with those, the user pays, and Razorpay hands the frontend back
//     razorpay_order_id / razorpay_payment_id / razorpay_signature.
//  2. The frontend POSTs those three fields to the app's verify endpoint
//     (routers.RazorpayVerifyHandler), which calls Verify() below to check
//     the HMAC signature and flip the plan_transactions row to "succeeded".
type RazorpayGateway struct {
	keyID     string
	keySecret string
	client    *razorpay.Client
}

func NewRazorpayGateway() interfaces.PaymentGateway {
	keyID := os.Getenv("RAZORPAY_KEY_ID")
	keySecret := os.Getenv("RAZORPAY_KEY_SECRET")
	return &RazorpayGateway{
		keyID:     keyID,
		keySecret: keySecret,
		client:    razorpay.NewClient(keyID, keySecret),
	}
}

func (g *RazorpayGateway) Name() string { return "razorpay" }

func (g *RazorpayGateway) Charge(ctx context.Context, req interfaces.ChargeRequest) (*interfaces.ChargeResult, error) {
	if g.keyID == "" || g.keySecret == "" {
		return nil, fmt.Errorf("razorpay: RAZORPAY_KEY_ID / RAZORPAY_KEY_SECRET not set")
	}
	if req.Amount <= 0 {
		return nil, fmt.Errorf("razorpay: amount must be positive")
	}

	currency := req.Currency
	if currency == "" {
		currency = "INR"
	}

	receipt := ""
	if req.Metadata != nil {
		receipt = req.Metadata["transactionId"]
	}

	notes := map[string]interface{}{}
	for k, v := range req.Metadata {
		notes[k] = v
	}

	data := map[string]interface{}{
		"amount":   int(req.Amount*100 + 0.5), // paise
		"currency": currency,
		"receipt":  receipt,
		"notes":    notes,
	}

	order, err := g.client.Order.Create(data, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay: create order: %w", err)
	}

	orderID, _ := order["id"].(string)
	if orderID == "" {
		return nil, fmt.Errorf("razorpay: order response missing id")
	}

	return &interfaces.ChargeResult{
		TransactionID:  orderID,
		Status:         "pending", // not paid until the frontend completes Checkout + we verify
		GatewayName:    g.Name(),
		GatewayOrderID: orderID,
		GatewayKeyID:   g.keyID,
	}, nil
}

// Verify checks the HMAC-SHA256 signature Razorpay Checkout returns after a
// successful payment, per Razorpay's documented scheme:
//
//	expected = HMAC_SHA256(order_id + "|" + payment_id, key_secret)
//
// req.Fields must contain "razorpay_order_id", "razorpay_payment_id", and
// "razorpay_signature".
func (g *RazorpayGateway) Verify(ctx context.Context, req interfaces.VerifyRequest) (*interfaces.ChargeResult, error) {
	orderID := req.Fields["razorpay_order_id"]
	paymentID := req.Fields["razorpay_payment_id"]
	signature := req.Fields["razorpay_signature"]

	if orderID == "" || paymentID == "" || signature == "" {
		return nil, fmt.Errorf("razorpay: missing razorpay_order_id/razorpay_payment_id/razorpay_signature")
	}

	if !verifyRazorpaySignature(orderID, paymentID, signature, g.keySecret) {
		return &interfaces.ChargeResult{
			TransactionID: paymentID,
			Status:        "failed",
			GatewayName:   g.Name(),
		}, nil
	}

	return &interfaces.ChargeResult{
		TransactionID: paymentID,
		Status:        "succeeded",
		GatewayName:   g.Name(),
	}, nil
}

func verifyRazorpaySignature(orderID, paymentID, signature, keySecret string) bool {
	payload := orderID + "|" + paymentID
	mac := hmac.New(sha256.New, []byte(keySecret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	// constant-time comparison to avoid timing side-channels
	return hmac.Equal([]byte(expected), []byte(signature))
}

// VerifyWebhookSignature checks the "X-Razorpay-Signature" header Razorpay
// sends on webhook events (payment.captured, payment.failed,
// refund.created, etc.) against RAZORPAY_WEBHOOK_SECRET.
//
// This is a *different* secret from RAZORPAY_KEY_SECRET — it's generated
// separately when you set up the webhook in the Razorpay Dashboard, and the
// verification scheme differs too: it's an HMAC over the raw request body,
// not over "order_id|payment_id" like the Checkout confirmation in
// verifyRazorpaySignature above.
func VerifyWebhookSignature(payload []byte, signature string) bool {
	secret := os.Getenv("RAZORPAY_WEBHOOK_SECRET")
	if secret == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
