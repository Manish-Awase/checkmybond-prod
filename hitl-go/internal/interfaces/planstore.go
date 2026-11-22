// internal/interfaces/planstore.go
//
// Pure interface + model definitions for plan transaction storage. No
// database driver is imported here on purpose — internal/planstore holds
// the Postgres/MySQL implementations, this file only defines the contract
// routers/payment.go is written against.
package interfaces

import "context"
import "time"

// PlanTransaction is one row: who paid, for which plan, how many fields it
// bought them, and what the payment gateway said happened.
type PlanTransaction struct {
	ID                   string
	UserID               string
	Username             string
	Email                string
	PlanID               string
	PlanLabel            string
	Quantity             int
	FieldsAllotted       int
	PricePerUnit         float64
	TotalAmount          float64
	Currency             string
	PaymentGateway       string
	GatewayTransactionID string
	// "pending" | "succeeded" | "failed" | "partially_refunded" | "refunded".
	// The two refund states are only ever set by the gateway webhooks
	// (routers/payment_webhooks.go) via RecordRefund -- never by the
	// purchase flow, and recording one never touches the user's token
	// balance (credits are deducted by hand, before refunding; see
	// REFUNDS_AND_EXPIRY_RUNBOOK.md).
	Status    string
	CreatedAt time.Time
	// RefundedAmount is what the gateway reports as refunded so far, in
	// this transaction's Currency (major units, e.g. dollars -- not
	// cents). 0 for anything never refunded. Populated by ListRefunds;
	// the other read paths don't need it and leave it zero.
	RefundedAmount float64
	RefundedAt     *time.Time
}

// PlanTransactionStore is implemented once per backing database
// (internal/planstore/postgres.go, internal/planstore/mysql.go, ...).
// routers/payment.go depends only on this interface, so swapping the
// database is a one-line change in main.go.
type PlanTransactionStore interface {
	// EnsureSchema creates the plan_transactions table if it doesn't exist yet.
	EnsureSchema(ctx context.Context) error

	InsertTransaction(ctx context.Context, t *PlanTransaction) error
	GetTransaction(ctx context.Context, id string) (*PlanTransaction, error)
	ListTransactionsByUser(ctx context.Context, userID string) ([]PlanTransaction, error)
	// ListRefunds is owner-tooling only (routers/account.go's /refunds
	// endpoint): every transaction with any refunded amount, across
	// every user, newest first. No per-user scope to check, unlike
	// ListTransactionsByUser.
	ListRefunds(ctx context.Context) ([]PlanTransaction, error)
	// GetByGatewayTransactionID finds a transaction by the gateway's own
	// payment id (Stripe PaymentIntent id / Razorpay payment id). Returns
	// (nil, nil) when nothing matches. This is the fallback the refund
	// webhooks use when the gateway event doesn't carry our own
	// transactionId -- e.g. a refund issued from the gateway's dashboard.
	GetByGatewayTransactionID(ctx context.Context, gatewayTransactionID string) (*PlanTransaction, error)
	// RecordRefund stores what the gateway says has been refunded so far.
	// refundedAmount is CUMULATIVE (not a delta), so a retried or
	// duplicated webhook is harmless. It only ever moves forward: the
	// amount can't go down and a fully refunded row never drops back to
	// partial, so an out-of-order older event can't undo a newer one. It
	// deliberately leaves gateway_transaction_id and the user's token
	// balance alone.
	RecordRefund(ctx context.Context, id string, refundedAmount float64, fullyRefunded bool) error

	// UpdateStatus is called once the payment gateway responds, to move a
	// row from "pending" to "succeeded"/"failed" and attach the gateway's
	// own transaction id.
	UpdateStatus(ctx context.Context, id, status, gatewayTransactionID string) error
}
