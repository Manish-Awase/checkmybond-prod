// internal/db/db.go
// Postgres connection pool + every query used by the routers.
// Go reads what Python writes and writes what Python reads —
// same tables, same column names as models/db.py.
package db

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── Pool ──────────────────────────────────────────────────────────────────────

var pool *pgxpool.Pool

func Init(ctx context.Context) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/hitl"
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse db config: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 1 * time.Minute

	pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create pool: %w", err)
	}
	return pool.Ping(ctx)
}

func Close() {
	if pool != nil {
		pool.Close()
	}
}

func Pool() *pgxpool.Pool { return pool }

// ── Models ────────────────────────────────────────────────────────────────────

type User struct {
	ID           string
	Username     string
	Email        string
	PasswordHash string
	Role         string
	IsActive     bool
	CreatedAt    time.Time
	LastLogin    *time.Time
	LastLogout   *time.Time
	DeletedAt    *time.Time
	Region       *string
	TokenBalance int
	// OrgID is nullable until CreateUser (below) is updated to accept and
	// set it at signup — see the matching comment on models/db.py's
	// User.org_id. Every query below already selects/scans it so the
	// column is available as soon as that INSERT is updated.
	OrgID *string
}

// Organization is the tenant root — users.org_id and
// template_definitions.org_id both FK to this table, not to each other,
// since a foreign key target must be unique and many users share one org.
type Organization struct {
	OrgID     string
	OrgName   string
	CreatedAt time.Time
}

// TemplateDefinition is a per-org document fingerprint + extraction rules
// cache. FingerprintJSON and RulesJSON are stored as raw JSON text on this
// side (json.RawMessage) — Postgres round-trips JSONB through pgx as text
// either way, so there is no need to unmarshal into a concrete Go struct
// unless a caller needs to inspect specific fields.
type TemplateDefinition struct {
	TemplateID      string
	OrgID           string
	BankName        string
	DocumentType    string
	FingerprintHash string
	FingerprintJSON json.RawMessage
	RulesJSON       json.RawMessage
	CreatedBy       string
	CreatedAt       time.Time
	LastUsedAt      *time.Time
	SuccessCount    int
	FailCount       int
}

type ExtractionJob struct {
	ExtractionID string
	DocHash      *string
	SchemaID     *string
	PdfPath      *string
	// ParsedContentJSON holds pdfplumber-parsed pages/tables for
	// desktop-sourced LLM-mode jobs (set via /jobs/{id}/parsed-content).
	// NULL for website-uploaded jobs and desktop OCR-mode jobs.
	ParsedContentJSON *string
	Status            string
	ResultJSON        *string
	Error             *string
	CreatedAt         time.Time
	StartedAt         *time.Time
	CompletedAt       *time.Time
	SLADeadline       time.Time
}

type Correction struct {
	ID                string
	ExtractionID      string
	DocHash           *string
	FieldName         string
	OriginalValue     *string
	CorrectedValue    string
	FailureType       string
	AnalystID         string
	SchemaID          *string
	PromptID          *string
	RetrievedChunkIDs *string
	ConfidenceScore   *float64
	ContextRelevance  *float64
	AnswerRelevance   *float64
	Groundedness      *float64
	CorrectedAt       time.Time
}

type AuditLog struct {
	ID          int64
	Path        string
	Method      string
	UserID      string
	QueryParams *string
	DurationMS  int
	StatusCode  int
	BodyHash    string
	Timestamp   time.Time
}

// ── Users ─────────────────────────────────────────────────────────────────────

// There is deliberately NO GetUserByUsername. A username is a display
// name, not an identity: several accounts may share one (see
// migrations/0006_username_not_unique.sql). A lookup by it can match many
// rows and QueryRow would silently return an arbitrary one -- so login,
// password reset, and every other "who is this" question go through the
// email (unique) or the user id, never the name.

// ErrEmailTaken is returned by CreateUser/CreateUserWithOrg when the
// email address is already registered. Email is the only unique identity
// on a user, so this is the one duplicate the database rejects.
var ErrEmailTaken = errors.New("db: email already registered")

// isEmailUniqueViolation reports whether err is Postgres rejecting a
// duplicate email -- as opposed to some other unique violation, which
// should NOT be reported to a user as "email already registered".
// Matches on the constraint/index name containing "email" (uq_users_email,
// ux_users_email_lower).
func isEmailUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return strings.Contains(strings.ToLower(pgErr.ConstraintName), "email")
	}
	return false
}

// GetUserByEmail finds the account for an email address, ignoring case.
// Case-insensitive on purpose: it is now the ONLY thing a person signs in
// with, and a legacy row stored before emails were normalized (mixed
// case) must not become unreachable. Safe because users has a unique
// index on lower(email), so at most one row can match -- ORDER BY/LIMIT
// below only makes the answer deterministic if that index were ever
// missing, it is not a substitute for it.
func GetUserByEmail(ctx context.Context, email string) (*User, error) {
	row := pool.QueryRow(ctx,
		`SELECT id, username, email, password_hash, role, is_active, created_at, last_login,
		        last_logout, deleted_at, region, token_balance, org_id
		 FROM users WHERE lower(email) = lower($1)
		 ORDER BY created_at ASC LIMIT 1`, email)
	return scanUser(row)
}

func GetUserByID(ctx context.Context, id string) (*User, error) {
	row := pool.QueryRow(ctx,
		`SELECT id, username, email, password_hash, role, is_active, created_at, last_login,
		        last_logout, deleted_at, region, token_balance, org_id
		 FROM users WHERE id = $1`, id)
	return scanUser(row)
}

func scanUser(row pgx.Row) (*User, error) {
	u := &User{}
	err := row.Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash,
		&u.Role, &u.IsActive, &u.CreatedAt, &u.LastLogin,
		&u.LastLogout, &u.DeletedAt, &u.Region, &u.TokenBalance, &u.OrgID,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return u, err
}

// CreateUser does NOT set org_id — no caller in this codebase currently
// supplies one. Signup stays open to unassigned-org users until
// routers/auth.go (and whatever collects org at registration) is wired
// to call CreateUserWithOrg instead. Left as-is deliberately rather than
// guessed at, since assigning every new signup to a real org depends on
// product decisions (self-serve org creation? invite-only? single org
// per email domain?) this file can't determine on its own.
func CreateUser(ctx context.Context, id, username, email, passwordHash, role string) (*User, error) {
	_, err := pool.Exec(ctx,
		`INSERT INTO users (id, username, email, password_hash, role, is_active, created_at, token_balance)
		 VALUES ($1, $2, $3, $4, $5, true, now(), 0)`,
		id, username, email, passwordHash, role,
	)
	if err != nil {
		if isEmailUniqueViolation(err) {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	return GetUserByID(ctx, id)
}

// CreateUserWithOrg is CreateUser plus org assignment — use this once the
// signup flow has an org_id to provide (existing org invite, or a newly
// created org — see CreateOrganization below).
func CreateUserWithOrg(ctx context.Context, id, username, email, passwordHash, role, orgID string) (*User, error) {
	_, err := pool.Exec(ctx,
		`INSERT INTO users (id, username, email, password_hash, role, is_active, created_at, token_balance, org_id)
		 VALUES ($1, $2, $3, $4, $5, true, now(), 0, $6)`,
		id, username, email, passwordHash, role, orgID,
	)
	if err != nil {
		if isEmailUniqueViolation(err) {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	return GetUserByID(ctx, id)
}

func UpdateLastLogin(ctx context.Context, userID string) error {
	_, err := pool.Exec(ctx,
		`UPDATE users SET last_login = now() WHERE id = $1`, userID)
	if err != nil {
		return err
	}
	// Best-effort history row for daily/weekly/monthly active-user counts
	// (table login_events in database/schema.sql). Failure here -- e.g. the table
	// is missing on an older database -- must never affect login, and the
	// caller already ignores this function's error.
	_, _ = pool.Exec(ctx,
		`INSERT INTO login_events (user_id) VALUES ($1)`, userID)
	return nil
}

// UpdateLastLogout — called from handleLogout. Best-effort: logout still
// succeeds (token still gets blacklisted) even if this write fails, since
// losing a logout *timestamp* is much less bad than failing to actually
// log the user out.
func UpdateLastLogout(ctx context.Context, userID string) error {
	_, err := pool.Exec(ctx,
		`UPDATE users SET last_logout = now() WHERE id = $1`, userID)
	return err
}

func SetUserActive(ctx context.Context, userID string, active bool) error {
	_, err := pool.Exec(ctx,
		`UPDATE users SET is_active = $1 WHERE id = $2`, active, userID)
	return err
}

// SoftDeleteUser marks an account deleted (deleted_at + is_active=false)
// without removing the row — audit_log, plan_transactions, and corrections
// all FK to users.id, and a hard delete would either cascade-destroy that
// history or fail on the constraint. deleted_at is what distinguishes a
// user-initiated deletion from an owner deactivation (is_active alone
// doesn't tell you which happened).
func SoftDeleteUser(ctx context.Context, userID string) error {
	_, err := pool.Exec(ctx,
		`UPDATE users SET deleted_at = now(), is_active = false WHERE id = $1`, userID)
	return err
}

// TokenGrant is one row of the credit ledger backing token_balance —
// see migrations/0005_token_grants.sql's comment for why this exists
// alongside the flat balance rather than instead of it.
type TokenGrant struct {
	ID              string
	UserID          string
	TransactionID   *string
	PlanID          string
	Source          string
	FieldsGranted   int
	FieldsRemaining int
	GrantedAt       time.Time
	ExpiresAt       *time.Time
}

func newGrantID() string {
	b := make([]byte, 16)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

// GrantTokens is how every REAL credit event (a purchase completing, a
// free-plan claim, a token-coupon redemption) should add to a user's
// balance — AddTokens below still exists, but only for refunds (see its
// own comment for why refunds deliberately don't go through here).
//
// Records a dated ledger row (token_grants) AND increments the cached
// users.token_balance, in one transaction, so the two can never drift
// apart. expiresAt is nil for anything that doesn't expire (free,
// per-field, bundle, coupon grants) — only "monthly" and "annual"
// purchases carry a real date, computed by the caller (see
// routers/payment.go's plan.ExpiresAfter).
func GrantTokens(ctx context.Context, userID, planID, source string, transactionID *string, amount int, expiresAt *time.Time) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO token_grants
		 (id, user_id, transaction_id, plan_id, source, fields_granted, fields_remaining, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$6,$7)`,
		newGrantID(), userID, transactionID, planID, source, amount, expiresAt,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET token_balance = token_balance + $1 WHERE id = $2`, amount, userID,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AddTokens increments token_balance directly, with NO grant record —
// meaning whatever it adds never expires. This is deliberately narrower
// than it used to be: it now exists only for REFUNDS (routers/documents.go,
// when an extraction fails after tokens were already deducted).
//
// A refund not carrying an expiry date is the safe default: reconstructing
// which grant(s) a specific deduction drew from, to refund into the same
// one with the same expiry, would need DeductTokens to return that
// information and every caller to thread it back through on failure --
// real, valuable work if refunds under this system turn out to be
// common, but not worth the complexity for what is meant to be a rare
// failure-recovery path today. Refunding as "never expires" costs
// nothing if refunds stay rare, and never means a bug silently expires
// money someone already spent successfully once.
func AddTokens(ctx context.Context, userID string, amount int) error {
	_, err := pool.Exec(ctx,
		`UPDATE users SET token_balance = token_balance + $1 WHERE id = $2`, amount, userID)
	return err
}

// DeductTokens atomically decrements token_balance, but only if the
// balance is actually sufficient, and consumes from the grant ledger in
// the same transaction so expiring credits get spent before ones that
// never lapse (soonest expires_at first, NULLs last — see
// idx_token_grants_consumption_order). Returns whether the deduction
// happened.
//
// Row locking (FOR UPDATE) makes this safe against two concurrent
// uploads racing each other, the same reasoning the old single-UPDATE
// version relied on Postgres's WHERE-at-update-time semantics for — a
// transaction with explicit locking is the multi-statement equivalent
// of that same guarantee.
//
// If the grant ledger doesn't fully cover `amount` — e.g. balance that
// pre-dates this feature, or drifted for any other reason — the
// shortfall is simply not attributed to any grant. That balance behaves
// as if it never expires, which is the conservative direction to fail
// in: better to occasionally under-expire than to ever expire fields a
// user never actually held past their real date.
func DeductTokens(ctx context.Context, userID string, amount int) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var balance int
	if err := tx.QueryRow(ctx,
		`SELECT token_balance FROM users WHERE id = $1 FOR UPDATE`, userID,
	).Scan(&balance); err != nil {
		return false, err
	}
	if balance < amount {
		return false, nil
	}

	rows, err := tx.Query(ctx,
		`SELECT id, fields_remaining FROM token_grants
		 WHERE user_id = $1 AND fields_remaining > 0 AND expired_at IS NULL
		 ORDER BY expires_at ASC NULLS LAST, granted_at ASC
		 FOR UPDATE`, userID)
	if err != nil {
		return false, err
	}
	type grantRow struct {
		id        string
		remaining int
	}
	var grants []grantRow
	for rows.Next() {
		var g grantRow
		if err := rows.Scan(&g.id, &g.remaining); err != nil {
			rows.Close()
			return false, err
		}
		grants = append(grants, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	left := amount
	for _, g := range grants {
		if left <= 0 {
			break
		}
		take := g.remaining
		if take > left {
			take = left
		}
		if _, err := tx.Exec(ctx,
			`UPDATE token_grants SET fields_remaining = fields_remaining - $1 WHERE id = $2`,
			take, g.id,
		); err != nil {
			return false, err
		}
		left -= take
	}
	// Any shortfall (left > 0) is untracked legacy/drifted balance — see
	// the function comment. Deliberately not an error.

	if _, err := tx.Exec(ctx,
		`UPDATE users SET token_balance = token_balance - $1 WHERE id = $2`, amount, userID,
	); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// ListActiveGrants returns a user's non-expired grants with fields still
// remaining, soonest-expiring first — what the "your tokens expire on
// DATE" UI reads from (routers/profile.go's /balance endpoint).
// Never-expiring grants are omitted entirely: there is nothing to warn
// about, and nothing for the user to act on.
func ListActiveGrants(ctx context.Context, userID string) ([]TokenGrant, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, user_id, transaction_id, plan_id, source, fields_granted, fields_remaining, granted_at, expires_at
		 FROM token_grants
		 WHERE user_id = $1 AND fields_remaining > 0 AND expired_at IS NULL AND expires_at IS NOT NULL
		 ORDER BY expires_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TokenGrant
	for rows.Next() {
		var g TokenGrant
		if err := rows.Scan(&g.ID, &g.UserID, &g.TransactionID, &g.PlanID, &g.Source,
			&g.FieldsGranted, &g.FieldsRemaining, &g.GrantedAt, &g.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SweepExpiredGrants reclaims every grant whose expiry has passed:
// zeroes its fields_remaining, marks it expired_at, and decrements the
// same amount from the owning user's cached token_balance — all per
// grant, in its own transaction, so one bad row can't block the rest of
// the sweep. Returns how many grants were reclaimed and the total fields
// removed, for the caller to log.
//
// Run on a ticker from cmd/server/main.go (see StartTokenGrantSweep) —
// this is what actually makes expiry happen; nothing else in this
// system removes fields from a balance on its own once granted.
func SweepExpiredGrants(ctx context.Context) (grantsReclaimed, fieldsReclaimed int, err error) {
	rows, err := pool.Query(ctx,
		`SELECT id, user_id, fields_remaining FROM token_grants
		 WHERE expires_at IS NOT NULL AND expires_at <= now()
		   AND expired_at IS NULL AND fields_remaining > 0`)
	if err != nil {
		return 0, 0, err
	}
	type expiredRow struct {
		id        string
		userID    string
		remaining int
	}
	var expired []expiredRow
	for rows.Next() {
		var e expiredRow
		if err := rows.Scan(&e.id, &e.userID, &e.remaining); err != nil {
			rows.Close()
			return grantsReclaimed, fieldsReclaimed, err
		}
		expired = append(expired, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return grantsReclaimed, fieldsReclaimed, err
	}

	for _, e := range expired {
		tx, txErr := pool.Begin(ctx)
		if txErr != nil {
			err = txErr
			continue
		}
		_, e1 := tx.Exec(ctx,
			`UPDATE token_grants SET fields_remaining = 0, expired_at = now() WHERE id = $1`, e.id)
		// GREATEST(...,0): a concurrent DeductTokens between the SELECT
		// above and this UPDATE could have already spent some of this
		// grant — never drive the cached balance negative reclaiming
		// fields that were, in the meantime, legitimately used.
		_, e2 := tx.Exec(ctx,
			`UPDATE users SET token_balance = GREATEST(token_balance - $1, 0) WHERE id = $2`,
			e.remaining, e.userID)
		if e1 != nil || e2 != nil {
			_ = tx.Rollback(ctx)
			if e1 != nil {
				err = e1
			} else {
				err = e2
			}
			continue
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			err = commitErr
			continue
		}
		grantsReclaimed++
		fieldsReclaimed += e.remaining
	}
	return grantsReclaimed, fieldsReclaimed, err
}

// StartTokenGrantSweep runs SweepExpiredGrants on a ticker until ctx is
// canceled. Call once from cmd/server/main.go's startup, alongside
// imetrics.StartHealthProbes:
//
//	idb.StartTokenGrantSweep(ctx, time.Hour)
//
// An hourly interval means a grant can sit expired-but-not-yet-reclaimed
// for up to an hour — token_balance briefly overstates what's actually
// usable. DeductTokens is still safe during that window (it never
// spends past what fields_remaining on non-expired-per-its-own-row
// grants actually holds), so this drift is cosmetic (the number shown
// to a user), not a way to spend fields that should already be gone.
// Shorten the interval if that window needs to be tighter.
func StartTokenGrantSweep(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n, fields, err := SweepExpiredGrants(ctx)
				if err != nil {
					fmt.Printf("token grant sweep: error reclaiming expired grants: %v\n", err)
					continue
				}
				if n > 0 {
					fmt.Printf("token grant sweep: reclaimed %d expired grant(s), %d field(s) total\n", n, fields)
				}
			}
		}
	}()
}

// GetTokenBalance — cheap standalone read, used by the frontend's
// upload-page balance display and by handleUploadOCR before it starts
// deciding how many files it can afford.
func GetTokenBalance(ctx context.Context, userID string) (int, error) {
	var balance int
	err := pool.QueryRow(ctx, `SELECT token_balance FROM users WHERE id = $1`, userID).Scan(&balance)
	return balance, err
}

func ListUsers(ctx context.Context) ([]User, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, username, email, password_hash, role, is_active, created_at, last_login,
		        last_logout, deleted_at, region, token_balance, org_id
		 FROM users ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u := User{}
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash,
			&u.Role, &u.IsActive, &u.CreatedAt, &u.LastLogin,
			&u.LastLogout, &u.DeletedAt, &u.Region, &u.TokenBalance, &u.OrgID); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ── Organizations ────────────────────────────────────────────────────────────

func CreateOrganization(ctx context.Context, orgID, orgName string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO organizations (org_id, org_name, created_at) VALUES ($1, $2, now())`,
		orgID, orgName,
	)
	return err
}

func GetOrganization(ctx context.Context, orgID string) (*Organization, error) {
	row := pool.QueryRow(ctx,
		`SELECT org_id, org_name, created_at FROM organizations WHERE org_id = $1`, orgID)
	o := &Organization{}
	err := row.Scan(&o.OrgID, &o.OrgName, &o.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return o, err
}

// ── Template Definitions ─────────────────────────────────────────────────────
// Every function here takes orgID explicitly and puts it in the WHERE
// clause, not just the SELECT list — the index alone does not enforce
// isolation, the query does. Never call Get/List without a caller-verified
// orgID (i.e. imw's authenticated user's own org, not a client-supplied one).

func CreateTemplateDefinition(ctx context.Context, t *TemplateDefinition) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO template_definitions
		 (template_id, org_id, bank_name, document_type, fingerprint_hash,
		  fingerprint_json, rules_json, created_by, created_at, success_count, fail_count)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now(), 0, 0)`,
		t.TemplateID, t.OrgID, t.BankName, t.DocumentType, t.FingerprintHash,
		t.FingerprintJSON, t.RulesJSON, t.CreatedBy,
	)
	return err
}

// GetTemplateByFingerprint is defined in preflight_queries.go instead —
// removed the duplicate here. Both versions existed only because they
// were added in separate turns without cross-checking; this one
// (returning *TemplateDefinition) was never actually called by any
// router, so nothing depends on removing it here.

// ListTemplatesForOrg returns every cached template for one org — used by
// an admin/management view. orgID must be the caller's own org; this
// function does not itself verify that, callers must.
func ListTemplatesForOrg(ctx context.Context, orgID string) ([]TemplateDefinition, error) {
	rows, err := pool.Query(ctx,
		`SELECT template_id, org_id, bank_name, document_type, fingerprint_hash,
		        fingerprint_json, rules_json, created_by, created_at, last_used_at,
		        success_count, fail_count
		 FROM template_definitions WHERE org_id = $1 ORDER BY created_at DESC`,
		orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var templates []TemplateDefinition
	for rows.Next() {
		t := TemplateDefinition{}
		if err := rows.Scan(
			&t.TemplateID, &t.OrgID, &t.BankName, &t.DocumentType, &t.FingerprintHash,
			&t.FingerprintJSON, &t.RulesJSON, &t.CreatedBy, &t.CreatedAt, &t.LastUsedAt,
			&t.SuccessCount, &t.FailCount,
		); err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	return templates, rows.Err()
}

// RecordTemplateOutcome bumps success_count or fail_count and touches
// last_used_at — call after a template's cached rules were actually
// applied to a real document, whichever way it went. orgID is required
// in the WHERE clause so a template_id guess/collision can't update a
// row belonging to a different org.
func RecordTemplateOutcome(ctx context.Context, orgID, templateID string, succeeded bool) error {
	col := "fail_count"
	if succeeded {
		col = "success_count"
	}
	_, err := pool.Exec(ctx,
		`UPDATE template_definitions
		 SET `+col+` = `+col+` + 1, last_used_at = now()
		 WHERE template_id = $1 AND org_id = $2`,
		templateID, orgID,
	)
	return err
}

// ── Extraction Jobs ───────────────────────────────────────────────────────────

func CreateExtractionJob(ctx context.Context, id, pdfPath, schemaID, docHash string, slaDeadline time.Time) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO extraction_jobs
		 (extraction_id, pdf_path, schema_id, doc_hash, status, created_at, sla_deadline)
		 VALUES ($1, $2, $3, $4, 'pending', now(), $5)`,
		id, pdfPath, schemaID, docHash, slaDeadline,
	)
	return err
}

func GetExtractionJob(ctx context.Context, id string) (*ExtractionJob, error) {
	row := pool.QueryRow(ctx,
		`SELECT extraction_id, doc_hash, schema_id, pdf_path, status,
		        result_json, error, created_at, started_at, completed_at, sla_deadline,
		        parsed_content_json
		 FROM extraction_jobs WHERE extraction_id = $1`, id)
	j := &ExtractionJob{}
	err := row.Scan(
		&j.ExtractionID, &j.DocHash, &j.SchemaID, &j.PdfPath, &j.Status,
		&j.ResultJSON, &j.Error, &j.CreatedAt, &j.StartedAt, &j.CompletedAt, &j.SLADeadline,
		&j.ParsedContentJSON,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return j, err
}

func ListPendingJobs(ctx context.Context) ([]ExtractionJob, error) {
	rows, err := pool.Query(ctx,
		`SELECT extraction_id, doc_hash, schema_id, pdf_path, status,
		        result_json, error, created_at, started_at, completed_at, sla_deadline,
		        parsed_content_json
		 FROM extraction_jobs
		 WHERE status IN ('pending','processing')
		 ORDER BY sla_deadline ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

func ListAllJobs(ctx context.Context) ([]ExtractionJob, error) {
	rows, err := pool.Query(ctx,
		`SELECT extraction_id, doc_hash, schema_id, pdf_path, status,
		        result_json, error, created_at, started_at, completed_at, sla_deadline,
		        parsed_content_json
		 FROM extraction_jobs ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

func scanJobs(rows pgx.Rows) ([]ExtractionJob, error) {
	var jobs []ExtractionJob
	for rows.Next() {
		j := ExtractionJob{}
		if err := rows.Scan(
			&j.ExtractionID, &j.DocHash, &j.SchemaID, &j.PdfPath, &j.Status,
			&j.ResultJSON, &j.Error, &j.CreatedAt, &j.StartedAt, &j.CompletedAt, &j.SLADeadline,
			&j.ParsedContentJSON,
		); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ── Corrections ───────────────────────────────────────────────────────────────

func CreateCorrection(ctx context.Context, c *Correction) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO corrections
		 (id, extraction_id, doc_hash, field_name, original_value, corrected_value,
		  failure_type, analyst_id, schema_id, prompt_id, retrieved_chunk_ids,
		  confidence_score, context_relevance, answer_relevance, groundedness, corrected_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15, now())`,
		c.ID, c.ExtractionID, c.DocHash, c.FieldName, c.OriginalValue,
		c.CorrectedValue, c.FailureType, c.AnalystID, c.SchemaID, c.PromptID,
		c.RetrievedChunkIDs, c.ConfidenceScore, c.ContextRelevance,
		c.AnswerRelevance, c.Groundedness,
	)
	return err
}

// ── Monitoring / Stats ────────────────────────────────────────────────────────

type CorrectionStats struct {
	Total         int            `json:"total"`
	AvgConfidence *float64       `json:"avg_confidence"`
	DPOPairsReady int            `json:"dpo_pairs_ready"`
	ByFailureType map[string]int `json:"by_failure_type"`
	ByPromptID    map[string]int `json:"by_prompt_id"`
}

func GetCorrectionStats(ctx context.Context) (*CorrectionStats, error) {
	stats := &CorrectionStats{
		ByFailureType: map[string]int{},
		ByPromptID:    map[string]int{},
	}

	// Total + avg confidence
	// ::float8 ensures pgx can scan into *float64 regardless of the table schema.
	row := pool.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(AVG(confidence_score)::float8, 0) FROM corrections`)
	if err := row.Scan(&stats.Total, &stats.AvgConfidence); err != nil {
		return nil, err
	}

	// DPO pairs = corrections with a prompt_id (ready to export)
	row = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM corrections WHERE prompt_id IS NOT NULL`)
	if err := row.Scan(&stats.DPOPairsReady); err != nil {
		return nil, err
	}

	// By failure type
	rows, err := pool.Query(ctx,
		`SELECT failure_type, COUNT(*) FROM corrections GROUP BY failure_type`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ft *string // nullable in case a NULL group exists
		var cnt int
		if err := rows.Scan(&ft, &cnt); err != nil {
			rows.Close()
			return nil, err
		}
		if ft != nil {
			stats.ByFailureType[*ft] = cnt
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// By prompt id
	rows2, err := pool.Query(ctx,
		`SELECT prompt_id, COUNT(*) FROM corrections
		 WHERE prompt_id IS NOT NULL GROUP BY prompt_id`)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var pid string
		var cnt int
		if err := rows2.Scan(&pid, &cnt); err != nil {
			return nil, err
		}
		stats.ByPromptID[pid] = cnt
	}
	if err := rows2.Err(); err != nil {
		return nil, err
	}

	return stats, nil
}

type DriftEntry struct {
	FieldName   string  `json:"field_name"`
	RecentAvg   float64 `json:"recent_avg"`
	BaselineAvg float64 `json:"baseline_avg"`
	Drift       float64 `json:"drift"`
	Alert       bool    `json:"alert"`
}

func GetDriftReport(ctx context.Context) ([]DriftEntry, error) {
	// Compare last 7 days vs prior 7 days per field
	rows, err := pool.Query(ctx, `
		SELECT
			field_name,
			AVG(CASE WHEN corrected_at >= now() - interval '7 days'
			         THEN confidence_score END) AS recent_avg,
			AVG(CASE WHEN corrected_at < now() - interval '7 days'
			         AND corrected_at >= now() - interval '14 days'
			         THEN confidence_score END) AS baseline_avg
		FROM corrections
		WHERE confidence_score IS NOT NULL
		GROUP BY field_name
		HAVING COUNT(*) >= 5
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []DriftEntry
	for rows.Next() {
		var e DriftEntry
		var recent, baseline *float64
		if err := rows.Scan(&e.FieldName, &recent, &baseline); err != nil {
			return nil, err
		}
		if recent == nil || baseline == nil {
			continue
		}
		e.RecentAvg = *recent
		e.BaselineAvg = *baseline
		e.Drift = *baseline - *recent // positive = degraded
		e.Alert = e.Drift > 0.1       // >10% drop triggers alert
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func GetQueueDepth(ctx context.Context) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM extraction_jobs WHERE status IN ('pending','processing')`).
		Scan(&n)
	return n, err
}

func GetEscalationCount(ctx context.Context) (int, error) {
	var n int
	// Escalations = jobs past their SLA deadline still not complete
	err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM extraction_jobs
		 WHERE status IN ('pending','processing') AND sla_deadline < now()`).
		Scan(&n)
	return n, err
}

// ── Audit Log ─────────────────────────────────────────────────────────────────

func WriteAuditLog(ctx context.Context, a *AuditLog) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO audit_log
		 (path, method, user_id, query_params, duration_ms, status_code, body_hash, timestamp)
		 VALUES ($1,$2,$3,$4,$5,$6,$7, now())`,
		a.Path, a.Method, a.UserID, a.QueryParams,
		a.DurationMS, a.StatusCode, a.BodyHash,
	)
	return err
}

// ── DB Backup ─────────────────────────────────────────────────────────────────

// BackupRow is a generic map used by the backup utility.
type BackupRow = map[string]any

func BackupTable(ctx context.Context, query string, args ...any) ([]BackupRow, error) {
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	var result []BackupRow
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make(BackupRow, len(fields))
		for i, f := range fields {
			row[string(f.Name)] = vals[i]
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// ── Field result parsing (used by extractions router) ────────────────────────

type FieldResult struct {
	FieldName        string   `json:"field_name"`
	Value            string   `json:"value"`
	ConfidenceScore  float64  `json:"confidence_score"`
	ContextRelevance float64  `json:"context_relevance"`
	Groundedness     float64  `json:"groundedness"`
	AnswerRelevance  float64  `json:"answer_relevance"`
	Decision         string   `json:"decision"`
	PromptID         string   `json:"prompt_id"`
	RetrievedChunks  []string `json:"retrieved_chunks"`
}

func ParseFieldResults(jsonStr string) ([]FieldResult, error) {
	var fields []FieldResult
	if err := json.Unmarshal([]byte(jsonStr), &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// ── Upload session types ──────────────────────────────────────────────────────

type UploadSessionFile struct {
	ID           string
	SessionID    string
	Filename     string
	DocHash      *string
	FileIndex    int
	Status       string // pending | queued | skipped | failed
	ExtractionID *string
	Error        *string
	SizeBytes    *int64
}

// ── Upload session queries ────────────────────────────────────────────────────

// CreateUploadSession creates a new upload session row.
// Called when the user starts a new folder upload.
func CreateUploadSession(ctx context.Context, sessionID, userID string, totalFiles int) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO upload_sessions
		 (session_id, user_id, total_files, queued_count, skipped_count, failed_count, status, created_at, updated_at)
		 VALUES ($1, $2, $3, 0, 0, 0, 'in_progress', now(), now())
		 ON CONFLICT (session_id) DO NOTHING`,
		sessionID, userID, totalFiles,
	)
	return err
}

// GetSessionQueuedHashes returns the set of doc_hashes already successfully
// queued in this session. Used for deduplication on resume — any file whose
// hash is in this set is skipped (it's already in the Celery queue).
func GetSessionQueuedHashes(ctx context.Context, sessionID string) (map[string]bool, error) {
	rows, err := pool.Query(ctx,
		`SELECT doc_hash FROM upload_session_files
		 WHERE session_id = $1 AND status = 'queued' AND doc_hash IS NOT NULL`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hashes := make(map[string]bool)
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err == nil {
			hashes[h] = true
		}
	}
	return hashes, nil
}

// GetSessionProgress returns (queued, skipped, failed, total) for a session.
func GetSessionProgress(ctx context.Context, sessionID string) (queued, skipped, failed, total int, err error) {
	err = pool.QueryRow(ctx,
		`SELECT queued_count, skipped_count, failed_count, total_files
		 FROM upload_sessions WHERE session_id = $1`,
		sessionID,
	).Scan(&queued, &skipped, &failed, &total)
	return
}

// UpsertSessionFile inserts or updates a single file record in the session.
func UpsertSessionFile(ctx context.Context, f UploadSessionFile) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO upload_session_files
		 (id, session_id, filename, doc_hash, file_index, status, extraction_id, error, size_bytes, processed_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		 ON CONFLICT (id) DO UPDATE SET
		   status        = EXCLUDED.status,
		   extraction_id = EXCLUDED.extraction_id,
		   error         = EXCLUDED.error,
		   processed_at  = now()`,
		f.ID, f.SessionID, f.Filename, f.DocHash, f.FileIndex,
		f.Status, f.ExtractionID, f.Error, f.SizeBytes,
	)
	return err
}

// UpdateSessionCounts increments the queued/skipped/failed counters atomically.
func UpdateSessionCounts(ctx context.Context, sessionID string, queued, skipped, failed int) error {
	_, err := pool.Exec(ctx,
		`UPDATE upload_sessions SET
		   queued_count  = queued_count  + $2,
		   skipped_count = skipped_count + $3,
		   failed_count  = failed_count  + $4,
		   updated_at    = now()
		 WHERE session_id = $1`,
		sessionID, queued, skipped, failed,
	)
	return err
}

// FinalizeSession marks a session complete or interrupted.
func FinalizeSession(ctx context.Context, sessionID, status string) error {
	_, err := pool.Exec(ctx,
		`UPDATE upload_sessions SET status = $2, updated_at = now()
		 WHERE session_id = $1`,
		sessionID, status,
	)
	return err
}

// GetSessionSummary returns a full session summary for the frontend.
func GetSessionSummary(ctx context.Context, sessionID string) (map[string]any, error) {
	var queuedC, skippedC, failedC, total int
	var status string
	err := pool.QueryRow(ctx,
		`SELECT queued_count, skipped_count, failed_count, total_files, status
		 FROM upload_sessions WHERE session_id = $1`,
		sessionID,
	).Scan(&queuedC, &skippedC, &failedC, &total, &status)
	if err != nil {
		return nil, err
	}

	remaining := total - queuedC - skippedC - failedC
	if remaining < 0 {
		remaining = 0
	}

	return map[string]any{
		"session_id":  sessionID,
		"total_files": total,
		"queued":      queuedC,
		"skipped":     skippedC,
		"failed":      failedC,
		"remaining":   remaining,
		"status":      status,
		"percent_done": func() int {
			if total == 0 {
				return 0
			}
			return int(float64(queuedC+skippedC+failedC) / float64(total) * 100)
		}(),
	}, nil
}

// ── System health (used by routers/system_health.go) ───────────────────────

// PingDB confirms Postgres is actually reachable and answering, not just
// that a pool object exists.
func PingDB(ctx context.Context) error {
	if pool == nil {
		return fmt.Errorf("db pool not initialized")
	}
	return pool.Ping(ctx)
}

// RequestHealth summarizes recent gateway/API traffic from audit_log —
// every request writes a row here (see LogAudit above), so this is a
// direct measure of what's actually happening, not a sampled metric.
type RequestHealth struct {
	TotalRequests int     `json:"totalRequests"`
	ErrorCount    int     `json:"errorCount"` // status_code >= 500
	ErrorRatePct  float64 `json:"errorRatePct"`
	AvgDurationMs float64 `json:"avgDurationMs"`
}

// GetRequestHealth looks at audit_log rows from the last `since` and
// summarizes error rate + average latency across all routes (login,
// documents, payment, everything — audit_log doesn't distinguish, that's
// on the caller to slice further if needed).
func GetRequestHealth(ctx context.Context, since time.Time) (*RequestHealth, error) {
	var total, errors int
	var avgMs float64
	err := pool.QueryRow(ctx, `
		SELECT
			count(*),
			count(*) FILTER (WHERE status_code >= 500),
			coalesce(avg(duration_ms), 0)
		FROM audit_log
		WHERE timestamp >= $1`, since).Scan(&total, &errors, &avgMs)
	if err != nil {
		return nil, err
	}
	rate := 0.0
	if total > 0 {
		rate = float64(errors) / float64(total) * 100
	}
	return &RequestHealth{
		TotalRequests: total,
		ErrorCount:    errors,
		ErrorRatePct:  rate,
		AvgDurationMs: avgMs,
	}, nil
}

// PaymentHealth summarizes recent plan_transactions rows — same idea as
// RequestHealth but scoped to payment activity specifically, since a
// generic error rate wouldn't necessarily surface "Stripe/Razorpay is
// rejecting everything right now" if payment failures come back as 200s
// with status:"failed" in the body rather than a 5xx.
type PaymentHealth struct {
	TotalAttempts  int     `json:"totalAttempts"`
	FailedCount    int     `json:"failedCount"`
	FailureRatePct float64 `json:"failureRatePct"`
}

func GetPaymentHealth(ctx context.Context, since time.Time) (*PaymentHealth, error) {
	var total, failed int
	err := pool.QueryRow(ctx, `
		SELECT
			count(*),
			count(*) FILTER (WHERE status = 'failed')
		FROM plan_transactions
		WHERE created_at >= $1`, since).Scan(&total, &failed)
	if err != nil {
		return nil, err
	}
	rate := 0.0
	if total > 0 {
		rate = float64(failed) / float64(total) * 100
	}
	return &PaymentHealth{
		TotalAttempts:  total,
		FailedCount:    failed,
		FailureRatePct: rate,
	}, nil
}

// ── Feedback / complaints (used by routers/profile.go) ─────────────────────

type Feedback struct {
	ID        string
	UserID    string
	Username  string
	Kind      string // "feedback" | "complaint" | "account_deletion"
	Message   string
	Rating    *int
	CreatedAt time.Time
}

// InsertFeedback writes to user_feedback. No FK to users(id) on purpose —
// account_deletion feedback is inserted in the same request that then
// soft-deletes the account, and a hard FK would create ordering fragility
// for no real benefit here (username is denormalized onto the row anyway,
// so the record stays readable even if the account is later deleted).
func InsertFeedback(ctx context.Context, f Feedback) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO user_feedback (id, user_id, username, kind, message, rating, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, now())`,
		f.ID, f.UserID, f.Username, f.Kind, f.Message, f.Rating,
	)
	return err
}
