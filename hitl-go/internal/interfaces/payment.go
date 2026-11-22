// internal/interfaces/payment.go
//
// Pure interface + model definitions for payment processing. No specific
// gateway is imported here — internal/payment holds the Mock/Stripe/
// Razorpay implementations, this file only defines the contract
// routers/payment.go is written against.
package interfaces

import "context"

// ChargeRequest is what routers/payment.go hands to whichever Gateway is
// wired up, regardless of which provider it actually is.
type ChargeRequest struct {
	UserID   string
	Email    string
	PlanID   string
	Amount   float64
	Currency string
	Metadata map[string]string

	// PaymentMethodID is optional. If the frontend already collected a
	// payment method (e.g. a Stripe PaymentMethod id from Stripe.js), pass
	// it here so the gateway can attempt to confirm the charge immediately.
	// If empty, gateways that require client-side collection (Stripe) or a
	// client-side checkout step (Razorpay) will return a "pending" result
	// with the info the frontend needs to complete the payment.
	PaymentMethodID string
}

// ChargeResult is what every Gateway implementation must return.
type ChargeResult struct {
	TransactionID string // the gateway's own id for this charge/order
	Status        string // "succeeded" | "failed" | "pending"
	GatewayName   string

	// The following are only populated by gateways whose payment flow
	// isn't a single synchronous server-side charge — i.e. the frontend
	// still has to do something (confirm a PaymentIntent, open a
	// checkout widget) before the payment is actually complete.

	// ClientSecret: Stripe PaymentIntent client_secret, used by
	// Stripe.js/Elements on the frontend to collect card details and
	// confirm the PaymentIntent.
	ClientSecret string

	// GatewayOrderID: Razorpay order id, passed to the Razorpay Checkout
	// widget on the frontend.
	GatewayOrderID string

	// GatewayKeyID: the gateway's *public* key (safe to expose to the
	// frontend), e.g. Razorpay's key_id or Stripe's publishable key.
	GatewayKeyID string
}

// PaymentGateway is implemented once per provider (internal/payment/mock.go,
// internal/payment/stripe.go, internal/payment/razorpay.go, ...).
// routers/payment.go depends only on this interface, so switching providers
// is a one-line change in main.go.
type PaymentGateway interface {
	Name() string
	Charge(ctx context.Context, req ChargeRequest) (*ChargeResult, error)
}

// VerifyRequest carries whatever fields a gateway's client-side flow hands
// back to the server for verification (e.g. Razorpay's
// order_id/payment_id/signature triple after its Checkout widget closes).
type VerifyRequest struct {
	TransactionID string // our internal plan_transactions id
	Fields        map[string]string
}

// Verifiable is implemented only by gateways whose Charge() can return a
// "pending" result that later needs a separate verification step (Razorpay
// today; Stripe could implement this too for webhook-based confirmation).
// routers/payment.go type-asserts the wired PaymentGateway against this
// interface rather than importing any concrete gateway package, so it stays
// decoupled from specific providers.
type Verifiable interface {
	Verify(ctx context.Context, req VerifyRequest) (*ChargeResult, error)
}
