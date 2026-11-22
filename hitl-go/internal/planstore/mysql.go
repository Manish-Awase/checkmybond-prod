// internal/planstore/mysql.go
package planstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/go-sql-driver/mysql"

	"hitl-platform/internal/interfaces"
)

type mysqlStore struct {
	db *sql.DB
}

// NewMySQLStore wraps a *sql.DB (opened with sql.Open("mysql", dsn), driver
// github.com/go-sql-driver/mysql) as an interfaces.PlanTransactionStore.
func NewMySQLStore(db *sql.DB) interfaces.PlanTransactionStore {
	return &mysqlStore{db: db}
}

func (s *mysqlStore) EnsureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS plan_transactions (
			id                     VARCHAR(64) PRIMARY KEY,
			user_id                VARCHAR(64) NOT NULL,
			username               VARCHAR(255),
			email                  VARCHAR(255),
			plan_id                VARCHAR(64) NOT NULL,
			plan_label             VARCHAR(255) NOT NULL,
			quantity               INT NOT NULL,
			fields_allotted        INT NOT NULL,
			price_per_unit         DECIMAL(10,2) NOT NULL,
			total_amount           DECIMAL(10,2) NOT NULL,
			currency               VARCHAR(8) NOT NULL DEFAULT 'USD',
			payment_gateway        VARCHAR(64) NOT NULL,
			gateway_transaction_id VARCHAR(255),
			status                 VARCHAR(32) NOT NULL,
			created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`)
	if err != nil {
		return err
	}

	// Same protection as the Postgres store's partial unique index (see
	// its EnsureSchema comment for why this needs to be a database-level
	// constraint, not just the read-then-write check in
	// ClaimFreeHandler) -- MySQL has no partial/filtered index, so a
	// generated column stands in for the WHERE clause: it's the user_id
	// for a successful free-plan row and NULL for everything else, and a
	// unique index on it rejects a second non-NULL value while letting
	// NULLs repeat freely.
	//
	// ADD COLUMN IF NOT EXISTS needs MySQL 8.0.29+; on an older server
	// this errors on every restart once the column exists. Tolerated
	// below (error 1060, duplicate column) rather than required, so this
	// degrades to "the constraint didn't get added" on very old MySQL
	// instead of breaking startup -- the fast-path check in the handler
	// still works, it just loses its backstop.
	if _, err := s.db.ExecContext(ctx, `
		ALTER TABLE plan_transactions
		ADD COLUMN free_claim_key VARCHAR(64)
		GENERATED ALWAYS AS (
			CASE WHEN plan_id = 'free' AND status = 'succeeded' THEN user_id ELSE NULL END
		) STORED`); err != nil && mysqlErrorCode(err) != 1060 {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE UNIQUE INDEX ux_plan_transactions_free_claim
		ON plan_transactions (free_claim_key)`); err != nil && mysqlErrorCode(err) != 1061 {
		return err
	}

	// Refund tracking -- same meaning as the Postgres store's columns
	// (see its EnsureSchema). Error 1060 = the columns already exist,
	// which is the normal case on every start after the first.
	if _, err := s.db.ExecContext(ctx, `
		ALTER TABLE plan_transactions
		ADD COLUMN refunded_amount DECIMAL(12,2) NOT NULL DEFAULT 0,
		ADD COLUMN refunded_at     TIMESTAMP NULL`); err != nil && mysqlErrorCode(err) != 1060 {
		return err
	}
	// Carry pre-existing 'refunded' rows forward as full refunds; only
	// touches rows still at the default 0, so a no-op after the first run.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE plan_transactions
		SET refunded_amount = total_amount, refunded_at = COALESCE(refunded_at, created_at)
		WHERE status = 'refunded' AND refunded_amount = 0`); err != nil {
		return err
	}
	return nil
}

// mysqlErrorCode extracts the numeric MySQL error code, or 0 if err isn't
// a *mysql.MySQLError (e.g. a context cancellation, or any other
// non-driver error).
func mysqlErrorCode(err error) int {
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) {
		return int(myErr.Number)
	}
	return 0
}

func (s *mysqlStore) InsertTransaction(ctx context.Context, t *interfaces.PlanTransaction) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO plan_transactions
		(id, user_id, username, email, plan_id, plan_label, quantity, fields_allotted,
		 price_per_unit, total_amount, currency, payment_gateway, gateway_transaction_id, status)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.UserID, t.Username, t.Email, t.PlanID, t.PlanLabel, t.Quantity, t.FieldsAllotted,
		t.PricePerUnit, t.TotalAmount, t.Currency, t.PaymentGateway, t.GatewayTransactionID, t.Status,
	)
	if err != nil {
		if mysqlErrorCode(err) == 1062 { // ER_DUP_ENTRY
			return ErrDuplicate
		}
		return err
	}
	return nil
}

func (s *mysqlStore) GetTransaction(ctx context.Context, id string) (*interfaces.PlanTransaction, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, username, email, plan_id, plan_label, quantity, fields_allotted,
		       price_per_unit, total_amount, currency, payment_gateway, gateway_transaction_id,
		       status, created_at, refunded_amount, refunded_at
		FROM plan_transactions WHERE id = ?`, id)

	t := &interfaces.PlanTransaction{}
	var refundedAt sql.NullTime
	err := row.Scan(&t.ID, &t.UserID, &t.Username, &t.Email, &t.PlanID, &t.PlanLabel, &t.Quantity,
		&t.FieldsAllotted, &t.PricePerUnit, &t.TotalAmount, &t.Currency, &t.PaymentGateway,
		&t.GatewayTransactionID, &t.Status, &t.CreatedAt, &t.RefundedAmount, &refundedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if refundedAt.Valid {
		ts := refundedAt.Time
		t.RefundedAt = &ts
	}
	return t, err
}

func (s *mysqlStore) ListTransactionsByUser(ctx context.Context, userID string) ([]interfaces.PlanTransaction, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, username, email, plan_id, plan_label, quantity, fields_allotted,
		       price_per_unit, total_amount, currency, payment_gateway, gateway_transaction_id,
		       status, created_at, refunded_amount, refunded_at
		FROM plan_transactions WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []interfaces.PlanTransaction
	for rows.Next() {
		t := interfaces.PlanTransaction{}
		var refundedAt sql.NullTime
		if err := rows.Scan(&t.ID, &t.UserID, &t.Username, &t.Email, &t.PlanID, &t.PlanLabel, &t.Quantity,
			&t.FieldsAllotted, &t.PricePerUnit, &t.TotalAmount, &t.Currency, &t.PaymentGateway,
			&t.GatewayTransactionID, &t.Status, &t.CreatedAt, &t.RefundedAmount, &refundedAt); err != nil {
			return nil, err
		}
		if refundedAt.Valid {
			ts := refundedAt.Time
			t.RefundedAt = &ts
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *mysqlStore) ListRefunds(ctx context.Context) ([]interfaces.PlanTransaction, error) {
	rows, err := s.db.QueryContext(ctx, `
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
		var refundedAt sql.NullTime
		if err := rows.Scan(&t.ID, &t.UserID, &t.Username, &t.Email, &t.PlanID, &t.PlanLabel, &t.Quantity,
			&t.FieldsAllotted, &t.PricePerUnit, &t.TotalAmount, &t.Currency, &t.PaymentGateway,
			&t.GatewayTransactionID, &t.Status, &t.CreatedAt, &t.RefundedAmount, &refundedAt); err != nil {
			return nil, err
		}
		if refundedAt.Valid {
			ts := refundedAt.Time
			t.RefundedAt = &ts
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *mysqlStore) GetByGatewayTransactionID(ctx context.Context, gatewayTransactionID string) (*interfaces.PlanTransaction, error) {
	if gatewayTransactionID == "" {
		return nil, nil // never match every row that has no gateway id yet
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, username, email, plan_id, plan_label, quantity, fields_allotted,
		       price_per_unit, total_amount, currency, payment_gateway, gateway_transaction_id,
		       status, created_at, refunded_amount, refunded_at
		FROM plan_transactions WHERE gateway_transaction_id = ?
		ORDER BY created_at DESC LIMIT 1`, gatewayTransactionID)

	t := &interfaces.PlanTransaction{}
	var refundedAt sql.NullTime
	err := row.Scan(&t.ID, &t.UserID, &t.Username, &t.Email, &t.PlanID, &t.PlanLabel, &t.Quantity,
		&t.FieldsAllotted, &t.PricePerUnit, &t.TotalAmount, &t.Currency, &t.PaymentGateway,
		&t.GatewayTransactionID, &t.Status, &t.CreatedAt, &t.RefundedAmount, &refundedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if refundedAt.Valid {
		ts := refundedAt.Time
		t.RefundedAt = &ts
	}
	return t, err
}

// RecordRefund -- see the interface comment for the contract.
func (s *mysqlStore) RecordRefund(ctx context.Context, id string, refundedAmount float64, fullyRefunded bool) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE plan_transactions
		SET refunded_amount = GREATEST(refunded_amount, ?),
		    refunded_at     = COALESCE(refunded_at, NOW()),
		    status          = CASE WHEN status = 'refunded' OR ? THEN 'refunded'
		                           ELSE 'partially_refunded' END
		WHERE id = ?`, refundedAmount, fullyRefunded, id)
	return err
}

func (s *mysqlStore) UpdateStatus(ctx context.Context, id, status, gatewayTransactionID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE plan_transactions SET status = ?, gateway_transaction_id = ? WHERE id = ?`,
		status, gatewayTransactionID, id)
	return err
}
