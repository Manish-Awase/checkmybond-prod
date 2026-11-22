// internal/payment/mock.go
package payment

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"hitl-platform/internal/interfaces"
)

// MockGateway always succeeds instantly. Useful for local dev/tests and as
// the default so the app runs out of the box without any provider keys.
type MockGateway struct{}

func NewMockGateway() interfaces.PaymentGateway { return &MockGateway{} }

func (g *MockGateway) Name() string { return "mock" }

func (g *MockGateway) Charge(ctx context.Context, req interfaces.ChargeRequest) (*interfaces.ChargeResult, error) {
	return &interfaces.ChargeResult{
		TransactionID: "mock_" + randomHex(),
		Status:        "succeeded",
		GatewayName:   g.Name(),
	}, nil
}

func randomHex() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
