// internal/payment/factory.go
//
// Factory only — the interface itself lives in internal/interfaces/payment.go.
package payment

import (
	"strings"

	"hitl-platform/internal/interfaces"
)

// NewGateway picks a PaymentGateway implementation by name
// (case-insensitive). Defaults to the mock gateway so the app runs out of
// the box.
//
//   - "stripe"   → StripeGateway   (needs STRIPE_SECRET_KEY, optionally
//     STRIPE_PUBLISHABLE_KEY for the frontend)
//   - "razorpay" → RazorpayGateway (needs RAZORPAY_KEY_ID + RAZORPAY_KEY_SECRET)
//   - anything else / unset → MockGateway (always succeeds instantly)
func NewGateway(name string) interfaces.PaymentGateway {
	switch strings.ToLower(name) {
	case "stripe":
		return NewStripeGateway()
	case "razorpay":
		return NewRazorpayGateway()
	default:
		return NewMockGateway()
	}
}
