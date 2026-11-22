// internal/planstore/postgres.go
package planstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"hitl-platform/internal/interfaces"
)

// ErrDuplicate is returned by InsertTransaction when a unique constraint
// blocked the insert — currently only the free-plan-per-user index. Kept
// generic (not "ErrAlreadyClaimedFree") because any store-level unique
// constraint should map to the same signal; callers decide what it means
// for their specific insert.
var ErrDuplicate = errors.New("planstore: duplicate — a unique constraint rejected this row")

type postgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore wraps an existing pool (e.g. internal/db.Pool()) as an
// interfaces.PlanTransactionStore.
func NewPostgresStore(pool *pgxpool.Pool) interfaces.PlanTransactionStore {
	return &postgresStore{pool: pool}
}

func (s *postgresStore) EnsureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS plan_transactions (
			id                     TEXT PRIMARY KEY,
			user_id                TEXT NOT NULL,
			username               TEXT,
			email                  TEXT,
			plan_id                TEXT NOT NULL,
			plan_label             TEXT NOT NULL,
			quantity               INTEGER NOT NULL,
			fields_allotted        INTEGER NOT NULL,
			price_per_unit         NUMERIC(10,2) NOT NULL,
			total_amount           NUMERIC(10,2) NOT NULL,
			currency               TEXT NOT NULL DEFAULT 'USD',
			payment_gateway        TEXT NOT NULL,
			gateway_transaction_id TEXT,
			status                 TEXT NOT NULL,
			created_at             TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return err
	}
	// Closes the race in ClaimFreeHandler: without this, two concurrent
	// requests can both pass its "have they already claimed?" read
	// before either write lands, and both get the free grant. A partial
	// unique index makes a second successful free-plan row for the same
	// user impossible at the database level, regardless of timing — the
	// check in the handler is now just a fast path for the common case,
	// not the actual guarantee. NULLs (failed/pending free rows, and
	// every non-free row) never conflict with each other or with this
	// index, so it only constrains what it's meant to.
	_, err = s.pool.Exec(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS ux_plan_transactions_free_claim
		ON plan_transactions (user_id)
		WHERE plan_id = 'free' AND status = 'succeeded'`)
	if err != nil {
		return err
	}

	// Refund tracking. refunded_amount is what the gateway says has been
	// refunded so far (cumulative, major units); status becomes
	// 'partially_refunded' or 'refunded' alongside it. Added with IF NOT
	// EXISTS so this is safe on a table created before these columns
	// existed.
	if _, err = s.pool.Exec(ctx, `
		ALTER TABLE plan_transactions
		ADD COLUMN IF NOT EXISTS refunded_amount NUMERIC(12,2) NOT NULL DEFAULT 0,
		ADD COLUMN IF NOT EXISTS refunded_at     TIMESTAMPTZ`); err != nil {
		return err
	}
	// Rows marked 'refunded' before refund amounts were tracked were, by
	// the old behavior, treated as full refunds -- carry that forward so
	// the owner dashboard doesn't show them as refunded for 0. Only
	// touches rows still at the default 0, so it's a no-op after the
	// first run and never overwrites a real recorded amount.
	_, err = s.pool.Exec(ctx, `
		UPDATE plan_transactions
		SET refunded_amount = total_amount, refunded_at = COALESCE(refunded_at, created_at)
		WHERE status = 'refunded' AND refunded_amount = 0`)
	return err
}

func (s *postgresStore) InsertTransaction(ctx context.Context, t *interfaces.PlanTransaction) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO plan_transactions
		(id, user_id, username, email, plan_id, plan_label, quantity, fields_allotted,
		 price_per_unit, total_amount, currency, payment_gateway, gateway_transaction_id,
		 status, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14, now())`,
		t.ID, t.UserID, t.Username, t.Email, t.PlanID, t.PlanLabel, t.Quantity, t.FieldsAllotted,
		t.PricePerUnit, t.TotalAmount, t.Currency, t.PaymentGateway, t.GatewayTransactionID, t.Status,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return ErrDuplicate
		}
		return err
	}
	return nil
}

func (s *postgresStore) GetTransaction(ctx context.Context, id string) (*interfaces.PlanTransaction, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, username, email, plan_id, plan_label, quantity, fields_allotted,
		       price_per_unit, total_amount, currency, payment_gateway, gateway_transaction_id,
		       status, created_at, refunded_amount, refunded_at
		FROM plan_transactions WHERE id = $1`, id)

	t := &interfaces.PlanTransaction{}
	err := row.Scan(&t.ID, &t.UserID, &t.Username, &t.Email, &t.PlanID, &t.PlanLabel, &t.Quantity,
		&t.FieldsAllotted, &t.PricePerUnit, &t.TotalAmount, &t.Currency, &t.PaymentGateway,
		&t.GatewayTransactionID, &t.Status, &t.CreatedAt, &t.RefundedAmount, &t.RefundedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (s *postgresStore) ListTransactionsByUser(ctx context.Context, userID string) ([]interfaces.PlanTransaction, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, username, email, plan_id, plan_label, quantity, fields_allotted,
		       price_per_unit, total_amount, currency, payment_gateway, gateway_transaction_id,
		       status, created_at, refunded_amount, refunded_at
		FROM plan_transactions WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []interfaces.PlanTransaction
	for rows.Next() {
		t := interfaces.PlanTransaction{}
		if err := rows.Scan(&t.ID, &t.UserID, &t.Username, &t.Email, &t.PlanID, &t.PlanLabel, &t.Quantity,
			&t.FieldsAllotted, &t.PricePerUnit, &t.TotalAmount, &t.Currency, &t.PaymentGateway,
			&t.GatewayTransactionID, &t.Status, &t.CreatedAt, &t.RefundedAmount, &t.RefundedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *postgresStore) ListRefunds(ctx context.Context) ([]interfaces.PlanTransaction, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, username, email, plan_id, plan_label, quantity, fields_allotted,
		       price_per_unit, total_amount, currency, payment_gateway, gateway_transaction_id,
		       status, created_at, refunded_amount, refunded_at
		FROM plan_transactions
		WHERE refunded_amount > 0 OR status IN ('refunded', 'partially_refunded')
		ORDER BY COALESCE(refunded_at, created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []interfaces.PlanTransaction
	for rows.Next() {
		t := interfaces.PlanTransaction{}
		if err := rows.Scan(&t.ID, &t.UserID, &t.Username, &t.Email, &t.PlanID, &t.PlanLabel, &t.Quantity,
			&t.FieldsAllotted, &t.PricePerUnit, &t.TotalAmount, &t.Currency, &t.PaymentGateway,
			&t.GatewayTransactionID, &t.Status, &t.CreatedAt, &t.RefundedAmount, &t.RefundedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *postgresStore) GetByGatewayTransactionID(ctx context.Context, gatewayTransactionID string) (*interfaces.PlanTransaction, error) {
	if gatewayTransactionID == "" {
		return nil, nil // never match every row that has no gateway id yet
	}
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, username, email, plan_id, plan_label, quantity, fields_allotted,
		       price_per_unit, total_amount, currency, payment_gateway, gateway_transaction_id,
		       status, created_at, refunded_amount, refunded_at
		FROM plan_transactions WHERE gateway_transaction_id = $1
		ORDER BY created_at DESC LIMIT 1`, gatewayTransactionID)

	t := &interfaces.PlanTransaction{}
	err := row.Scan(&t.ID, &t.UserID, &t.Username, &t.Email, &t.PlanID, &t.PlanLabel, &t.Quantity,
		&t.FieldsAllotted, &t.PricePerUnit, &t.TotalAmount, &t.Currency, &t.PaymentGateway,
		&t.GatewayTransactionID, &t.Status, &t.CreatedAt, &t.RefundedAmount, &t.RefundedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return t, err
}

// RecordRefund -- see the interface comment for the contract. The
// GREATEST/CASE make it monotonic and idempotent in one statement, so
// there is no read-modify-write for two concurrent webhooks to race on.
func (s *postgresStore) RecordRefund(ctx context.Context, id string, refundedAmount float64, fullyRefunded bool) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE plan_transactions
		SET refunded_amount = GREATEST(refunded_amount, $2),
		    refunded_at     = COALESCE(refunded_at, now()),
		    status          = CASE WHEN status = 'refunded' OR $3 THEN 'refunded'
		                           ELSE 'partially_refunded' END
		WHERE id = $1`, id, refundedAmount, fullyRefunded)
	return err
}

func (s *postgresStore) UpdateStatus(ctx context.Context, id, status, gatewayTransactionID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE plan_transactions SET status = $1, gateway_transaction_id = $2 WHERE id = $3`,
		status, gatewayTransactionID, id)
	return err
}
