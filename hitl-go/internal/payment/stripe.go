// internal/payment/stripe.go
package payment

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/paymentintent"
	"github.com/stripe/stripe-go/v76/webhook"

	"hitl-platform/internal/interfaces"
)

// StripeGateway creates and (where possible) confirms a Stripe
// PaymentIntent per charge. Nothing outside this file needs to change:
// routers/payment.go and internal/planstore only ever see the
// interfaces.PaymentGateway interface.
//
// Important reality check: Stripe cannot charge a card the server has
// never seen. If the caller doesn't pass a PaymentMethodID (i.e. the
// frontend hasn't collected card details via Stripe.js/Elements yet),
// this returns a "pending" ChargeResult with a ClientSecret — the
// frontend must use that to finish the payment client-side
// (stripe.confirmCardPayment(clientSecret, ...)). Only once that
// succeeds is the PaymentIntent actually "succeeded".
type StripeGateway struct {
	secretKey string
}

func NewStripeGateway() interfaces.PaymentGateway {
	return &StripeGateway{secretKey: os.Getenv("STRIPE_SECRET_KEY")}
}

func (g *StripeGateway) Name() string { return "stripe" }

func (g *StripeGateway) Charge(ctx context.Context, req interfaces.ChargeRequest) (*interfaces.ChargeResult, error) {
	if g.secretKey == "" {
		return nil, fmt.Errorf("stripe: STRIPE_SECRET_KEY is not set")
	}
	if req.Amount <= 0 {
		return nil, fmt.Errorf("stripe: amount must be positive")
	}

	stripe.Key = g.secretKey

	params := &stripe.PaymentIntentParams{
		Amount:             stripe.Int64(int64(req.Amount*100 + 0.5)), // Stripe wants the smallest currency unit (cents)
		Currency:           stripe.String(strings.ToLower(req.Currency)),
		Metadata:           req.Metadata,
		PaymentMethodTypes: stripe.StringSlice([]string{"card"}),
	}
	if req.Email != "" {
		params.ReceiptEmail = stripe.String(req.Email)
	}
	if req.PaymentMethodID != "" {
		// A payment method was already collected client-side — attempt to
		// confirm immediately.
		params.PaymentMethod = stripe.String(req.PaymentMethodID)
		params.Confirm = stripe.Bool(true)
		params.OffSession = stripe.Bool(true)
	}

	pi, err := paymentintent.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe: create payment intent: %w", err)
	}

	status := mapStripeStatus(pi.Status)

	return &interfaces.ChargeResult{
		TransactionID: pi.ID,
		Status:        status,
		GatewayName:   g.Name(),
		ClientSecret:  pi.ClientSecret, // empty once truly succeeded, but harmless to include
		GatewayKeyID:  os.Getenv("STRIPE_PUBLISHABLE_KEY"),
	}, nil
}

// mapStripeStatus maps Stripe's PaymentIntent status vocabulary onto the
// three-value status ("succeeded" | "pending" | "failed") the rest of the
// app expects.
func mapStripeStatus(s stripe.PaymentIntentStatus) string {
	switch s {
	case stripe.PaymentIntentStatusSucceeded:
		return "succeeded"
	case stripe.PaymentIntentStatusCanceled:
		return "failed"
	default:
		// requires_payment_method, requires_confirmation, requires_action,
		// processing, requires_capture — all mean "not done yet, frontend
		// still has a step to complete via the client secret".
		return "pending"
	}
}

// ConstructWebhookEvent verifies the "Stripe-Signature" header against
// STRIPE_WEBHOOK_SECRET (the signing secret shown in the Stripe Dashboard
// for this specific webhook endpoint — NOT the same value as
// STRIPE_SECRET_KEY) and parses the event on success. payload must be the
// raw, unparsed request body — signature verification fails if it's been
// re-encoded in any way.
func ConstructWebhookEvent(payload []byte, sigHeader string) (*stripe.Event, error) {
	secret := os.Getenv("STRIPE_WEBHOOK_SECRET")
	if secret == "" {
		return nil, fmt.Errorf("stripe: STRIPE_WEBHOOK_SECRET is not set")
	}
	event, err := webhook.ConstructEvent(payload, sigHeader, secret)
	if err != nil {
		return nil, fmt.Errorf("stripe: signature verification failed: %w", err)
	}
	return &event, nil
}
