// internal/coupon/store.go
//
// Coupon generation, validation and redemption.
//
// THE ONE THING THAT MATTERS HERE IS THAT REDEMPTION IS ATOMIC.
//
// A coupon has finite redemptions. Validation and redemption are
// therefore not the same operation and must not share a code path:
// Evaluate() is a read-only preview that can be called freely (on every
// keystroke in the checkout box, if you like) and is allowed to be
// slightly stale; Redeem() takes a row lock, re-checks every condition
// against the locked row, and only then increments. Two users racing for
// the last redemption of a single-use code cannot both win, because the
// second transaction blocks on SELECT ... FOR UPDATE until the first
// commits and then sees redeemed_count already at the limit.
//
// The tempting shortcut — validate in Go, then UPDATE — is a
// read-then-write race that lets a code with max_redemptions=1 be
// redeemed by however many requests arrive in the same millisecond. That
// is a real, routinely exploited bug in coupon systems, not a
// theoretical one, which is why the lock is here rather than left to the
// caller to remember.
package coupon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── Kinds ────────────────────────────────────────────────────────────────────

const (
	KindPercent = "percent" // N% off the order
	KindFlat    = "flat"    // N currency units off the order
	KindTokens  = "tokens"  // N field credits granted directly, no payment
)

func ValidKind(k string) bool {
	switch k {
	case KindPercent, KindFlat, KindTokens:
		return true
	}
	return false
}

// ── Errors ───────────────────────────────────────────────────────────────────
//
// Typed so the router can map each to an HTTP status and a metric label
// without string-matching. The messages are written to be shown to the
// end user as-is — they say what is wrong and, where there is one, what
// to do instead.

var (
	ErrNotFound       = errors.New("that coupon code doesn't exist")
	ErrInactive       = errors.New("this coupon has been switched off")
	ErrExpired        = errors.New("this coupon has expired")
	ErrNotStarted     = errors.New("this coupon isn't active yet")
	ErrExhausted      = errors.New("this coupon has been fully claimed")
	ErrAlreadyUsed    = errors.New("you've already used this coupon")
	ErrPlanNotAllowed = errors.New("this coupon doesn't apply to the selected plan")
	ErrBelowMinimum   = errors.New("your order is below this coupon's minimum")
	ErrWrongKind      = errors.New("this coupon can't be used that way")
)

// Reason maps an error to a short, bounded label for metrics. Bounded is
// the operative word — err.Error() on a metric label would be unbounded
// cardinality.
func Reason(err error) string {
	switch {
	case err == nil:
		return "accepted"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrInactive):
		return "inactive"
	case errors.Is(err, ErrExpired):
		return "expired"
	case errors.Is(err, ErrNotStarted):
		return "not_started"
	case errors.Is(err, ErrExhausted):
		return "exhausted"
	case errors.Is(err, ErrAlreadyUsed):
		return "already_used"
	case errors.Is(err, ErrPlanNotAllowed):
		return "plan_not_allowed"
	case errors.Is(err, ErrBelowMinimum):
		return "below_minimum"
	case errors.Is(err, ErrWrongKind):
		return "wrong_kind"
	default:
		return "internal"
	}
}

// ── Model ────────────────────────────────────────────────────────────────────

type Coupon struct {
	Code           string     `json:"code"`
	Kind           string     `json:"kind"`
	Value          float64    `json:"value"`
	Currency       string     `json:"currency"`
	AppliesToPlans []string   `json:"appliesToPlans"`
	MinAmount      float64    `json:"minAmount"`
	MaxDiscount    *float64   `json:"maxDiscount,omitempty"`
	MaxRedemptions *int       `json:"maxRedemptions,omitempty"`
	MaxPerUser     int        `json:"maxRedemptionsPerUser"`
	RedeemedCount  int        `json:"redeemedCount"`
	StartsAt       time.Time  `json:"startsAt"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
	IsActive       bool       `json:"isActive"`
	BatchID        *string    `json:"batchId,omitempty"`
	Campaign       *string    `json:"campaign,omitempty"`
	Notes          *string    `json:"notes,omitempty"`
	CreatedBy      string     `json:"createdBy"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// Redemption is one recorded use.
type Redemption struct {
	ID             string    `json:"id"`
	Code           string    `json:"code"`
	UserID         string    `json:"userId"`
	Username       *string   `json:"username,omitempty"`
	Email          *string   `json:"email,omitempty"`
	TransactionID  *string   `json:"transactionId,omitempty"`
	DiscountAmount float64   `json:"discountAmount"`
	TokensGranted  int       `json:"tokensGranted"`
	OriginalAmount float64   `json:"originalAmount"`
	FinalAmount    float64   `json:"finalAmount"`
	Currency       string    `json:"currency"`
	RedeemedAt     time.Time `json:"redeemedAt"`
}

// Evaluation is what a coupon would do to a given order — the result of
// a read-only preview.
type Evaluation struct {
	Code           string  `json:"code"`
	Kind           string  `json:"kind"`
	Valid          bool    `json:"valid"`
	Reason         string  `json:"reason,omitempty"`
	OriginalAmount float64 `json:"originalAmount"`
	DiscountAmount float64 `json:"discountAmount"`
	FinalAmount    float64 `json:"finalAmount"`
	TokensGranted  int     `json:"tokensGranted"`
	Currency       string  `json:"currency"`
	Description    string  `json:"description"`
}

// ── Store ────────────────────────────────────────────────────────────────────

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const couponColumns = `code, kind, value, currency, applies_to_plans, min_amount, max_discount,
	max_redemptions, max_redemptions_per_user, redeemed_count, starts_at, expires_at,
	is_active, batch_id, campaign, notes, created_by, created_at, updated_at`

func scanCoupon(row pgx.Row) (*Coupon, error) {
	var c Coupon
	var plans *string
	err := row.Scan(
		&c.Code, &c.Kind, &c.Value, &c.Currency, &plans, &c.MinAmount, &c.MaxDiscount,
		&c.MaxRedemptions, &c.MaxPerUser, &c.RedeemedCount, &c.StartsAt, &c.ExpiresAt,
		&c.IsActive, &c.BatchID, &c.Campaign, &c.Notes, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.AppliesToPlans = splitPlans(plans)
	return &c, nil
}

func splitPlans(s *string) []string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	parts := strings.Split(*s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func joinPlans(plans []string) *string {
	if len(plans) == 0 {
		return nil
	}
	joined := strings.Join(plans, ",")
	return &joined
}

// ── Code generation ──────────────────────────────────────────────────────────

// codeAlphabet deliberately omits 0/O, 1/I/L and U.
//
// The first two are the classic misreads when someone types a code off a
// printed card or a conference badge, and every such misread becomes a
// support ticket. U is dropped because excluding it makes it very hard
// for the generator to accidentally produce a recognisable obscenity in
// the middle of a code, which is a genuinely embarrassing way to discover
// your random generator works.
const codeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

// GenerateCode returns prefix + "-" + n random characters. An empty
// prefix produces just the random part.
func GenerateCode(prefix string, n int) (string, error) {
	if n <= 0 {
		n = 8
	}
	b := make([]byte, n)
	max := big.NewInt(int64(len(codeAlphabet)))
	for i := range b {
		// crypto/rand, not math/rand: a predictable coupon code is a
		// coupon code that can be guessed in bulk by anyone who sees one.
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate coupon code: %w", err)
		}
		b[i] = codeAlphabet[idx.Int64()]
	}
	prefix = NormalizeCode(prefix)
	if prefix == "" {
		return string(b), nil
	}
	return prefix + "-" + string(b), nil
}

// NormalizeCode is the single definition of how a code is stored and
// compared: uppercase, trimmed, internal whitespace removed.
//
// Applied on every write AND every lookup, so a user typing
// "  summer 24 " matches the stored SUMMER24 instead of getting "that
// coupon code doesn't exist" and giving up.
func NormalizeCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	return strings.Join(strings.Fields(code), "")
}

// ── Create ───────────────────────────────────────────────────────────────────

// Validate checks a coupon definition before it is written. Returns a
// user-facing message on failure.
func (c *Coupon) Validate() error {
	c.Code = NormalizeCode(c.Code)
	if c.Code == "" {
		return errors.New("a coupon needs a code")
	}
	if len(c.Code) > 64 {
		return errors.New("coupon codes are limited to 64 characters")
	}
	if !ValidKind(c.Kind) {
		return fmt.Errorf("unknown coupon kind %q — use percent, flat or tokens", c.Kind)
	}
	if c.Value <= 0 {
		return errors.New("a coupon's value must be greater than zero")
	}
	if c.Kind == KindPercent && c.Value > 100 {
		return errors.New("a percentage coupon can't be more than 100")
	}
	if c.Kind == KindTokens && c.Value != float64(int(c.Value)) {
		return errors.New("token coupons grant whole fields, so the value can't have decimals")
	}
	if c.MaxPerUser <= 0 {
		c.MaxPerUser = 1
	}
	if c.MaxRedemptions != nil && *c.MaxRedemptions <= 0 {
		return errors.New("the total redemption limit must be at least 1, or left empty for unlimited")
	}
	if c.MaxRedemptions == nil {
		// A coupon the owner didn't explicitly cap defaults to
		// single-use (one redemption, total, across every user) rather
		// than unlimited. This is what "generate a coupon" should mean
		// by default — a single-use grant, not an open code anyone can
		// pass around indefinitely. Unlimited (or any higher number) is
		// still available by setting maxRedemptions explicitly, e.g.
		// for a broadly-shared marketing code the owner deliberately
		// wants many people to redeem.
		one := 1
		c.MaxRedemptions = &one
	}
	if c.ExpiresAt != nil && c.ExpiresAt.Before(c.StartsAt) {
		return errors.New("this coupon would expire before it starts")
	}
	if c.Currency == "" {
		c.Currency = "USD"
	}
	if c.MinAmount < 0 {
		return errors.New("the minimum order amount can't be negative")
	}
	return nil
}

// Create inserts one coupon. Returns a clear conflict error when the code
// is taken, since "pick another code" is the only useful response.
func (s *Store) Create(ctx context.Context, c *Coupon) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.StartsAt.IsZero() {
		c.StartsAt = time.Now()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO coupons (`+couponColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,0,$10,$11,$12,$13,$14,$15,$16, now(), now())`,
		c.Code, c.Kind, c.Value, c.Currency, joinPlans(c.AppliesToPlans), c.MinAmount, c.MaxDiscount,
		c.MaxRedemptions, c.MaxPerUser, c.StartsAt, c.ExpiresAt,
		c.IsActive, c.BatchID, c.Campaign, c.Notes, c.CreatedBy,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
			return fmt.Errorf("the code %s is already in use", c.Code)
		}
		return err
	}
	return nil
}

// bulkChunkSize is how many coupons go into one INSERT. Large enough
// that a 2,000-code batch is four round trips rather than 2,000, small
// enough to stay well under Postgres's 65535-parameter statement limit
// (16 parameters per row × 500 = 8,000).
const bulkChunkSize = 500

// CreateBulk generates `count` coupons sharing one template and one
// batch_id, and returns the codes.
//
// Inserted in chunks rather than one row at a time. The difference is not
// cosmetic: a 2,000-code batch as individual INSERTs is 2,000 network
// round trips, which on a managed database easily exceeds the 60-second
// request timeout in cmd/server/main.go and leaves a half-created batch
// behind. Chunked, the same batch is four statements.
//
// Collisions are handled by ON CONFLICT DO NOTHING plus a retry for
// whatever did not land. With a 30-character alphabet an 8-character
// suffix is about 6.5e11 possibilities, so a collision is vanishingly
// unlikely — but RETURNING tells us exactly which rows were actually
// written, so we never report a code to the admin that is not in the
// table.
func (s *Store) CreateBulk(ctx context.Context, template Coupon, count int, prefix string, codeLen int) ([]string, error) {
	if count <= 0 {
		return nil, errors.New("how many coupons? the count must be at least 1")
	}
	if count > 5000 {
		return nil, errors.New("that's more than 5000 coupons — split it into smaller batches")
	}
	if template.BatchID == nil || *template.BatchID == "" {
		batch := "batch_" + time.Now().UTC().Format("20060102T150405")
		template.BatchID = &batch
	}
	if template.StartsAt.IsZero() {
		template.StartsAt = time.Now()
	}
	// Validate the template once, on a throwaway copy carrying a
	// placeholder code, so a bad definition fails before any row is
	// written rather than halfway through the batch.
	probe := template
	probe.Code = "PROBE1"
	if err := probe.Validate(); err != nil {
		return nil, err
	}
	template.Currency = probe.Currency
	template.MaxPerUser = probe.MaxPerUser
	// Validate defaults an unset MaxRedemptions to single-use (see its
	// comment) by mutating probe, not template -- copy it back, or every
	// code in the batch would keep template's original nil (unlimited)
	// regardless of what the single-coupon path defaults to.
	template.MaxRedemptions = probe.MaxRedemptions

	created := make([]string, 0, count)
	for attempt := 0; len(created) < count && attempt < 5; attempt++ {
		remaining := count - len(created)
		for remaining > 0 {
			chunk := remaining
			if chunk > bulkChunkSize {
				chunk = bulkChunkSize
			}
			codes := make([]string, 0, chunk)
			for i := 0; i < chunk; i++ {
				code, err := GenerateCode(prefix, codeLen)
				if err != nil {
					return created, err
				}
				codes = append(codes, code)
			}
			landed, err := s.insertChunk(ctx, template, codes)
			if err != nil {
				return created, err
			}
			created = append(created, landed...)
			remaining -= chunk
		}
	}
	if len(created) < count {
		return created, fmt.Errorf("only %d of %d codes could be generated — try a different prefix", len(created), count)
	}
	return created, nil
}

// insertChunk writes one multi-row INSERT and returns the codes that were
// actually stored.
func (s *Store) insertChunk(ctx context.Context, t Coupon, codes []string) ([]string, error) {
	if len(codes) == 0 {
		return nil, nil
	}
	const perRow = 16
	args := make([]any, 0, len(codes)*perRow)
	var sb strings.Builder
	sb.WriteString(`INSERT INTO coupons (` + couponColumns + `) VALUES `)

	plans := joinPlans(t.AppliesToPlans)
	for i, code := range codes {
		if i > 0 {
			sb.WriteByte(',')
		}
		b := i * perRow
		// redeemed_count is literal 0 and the two timestamps are now();
		// only the 16 varying columns are parameters.
		fmt.Fprintf(&sb,
			"($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,0,$%d,$%d,$%d,$%d,$%d,$%d,$%d, now(), now())",
			b+1, b+2, b+3, b+4, b+5, b+6, b+7, b+8, b+9, b+10, b+11, b+12, b+13, b+14, b+15, b+16)
		args = append(args,
			code, t.Kind, t.Value, t.Currency, plans, t.MinAmount, t.MaxDiscount,
			t.MaxRedemptions, t.MaxPerUser, t.StartsAt, t.ExpiresAt,
			t.IsActive, t.BatchID, t.Campaign, t.Notes, t.CreatedBy)
	}
	sb.WriteString(` ON CONFLICT (code) DO NOTHING RETURNING code`)

	rows, err := s.pool.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0, len(codes))
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ── Read ─────────────────────────────────────────────────────────────────────

func (s *Store) Get(ctx context.Context, code string) (*Coupon, error) {
	return scanCoupon(s.pool.QueryRow(ctx,
		`SELECT `+couponColumns+` FROM coupons WHERE code = $1`, NormalizeCode(code)))
}

// ListFilter narrows List. Zero value means "everything".
type ListFilter struct {
	BatchID    string
	Campaign   string
	Kind       string
	ActiveOnly bool
	Search     string
	Limit      int
	Offset     int
}

func (s *Store) List(ctx context.Context, f ListFilter) ([]Coupon, error) {
	// Conditions are built as parameterized fragments — the only things
	// interpolated into the SQL string are placeholder numbers this
	// function generates itself, never any caller-supplied value.
	where := []string{"1=1"}
	args := []any{}
	add := func(cond string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.BatchID != "" {
		add("batch_id = $%d", f.BatchID)
	}
	if f.Campaign != "" {
		add("campaign = $%d", f.Campaign)
	}
	if f.Kind != "" {
		add("kind = $%d", f.Kind)
	}
	if f.ActiveOnly {
		where = append(where, "is_active = true AND (expires_at IS NULL OR expires_at > now())")
	}
	if f.Search != "" {
		add("code LIKE $%d", "%"+NormalizeCode(f.Search)+"%")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	args = append(args, limit)
	limitPlaceholder := len(args)
	args = append(args, f.Offset)
	offsetPlaceholder := len(args)

	q := fmt.Sprintf(`SELECT %s FROM coupons WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`,
		couponColumns, strings.Join(where, " AND "), limitPlaceholder, offsetPlaceholder)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Coupon{}
	for rows.Next() {
		c, err := scanCoupon(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// CountUserRedemptions counts how many times one user has used one code.
func (s *Store) CountUserRedemptions(ctx context.Context, code, userID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM coupon_redemptions WHERE code = $1 AND user_id = $2`,
		NormalizeCode(code), userID).Scan(&n)
	return n, err
}

func (s *Store) ListRedemptions(ctx context.Context, code string, limit int) ([]Redemption, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, code, user_id, username, email, transaction_id, discount_amount,
		       tokens_granted, original_amount, final_amount, currency, redeemed_at
		FROM coupon_redemptions WHERE code = $1 ORDER BY redeemed_at DESC LIMIT $2`,
		NormalizeCode(code), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Redemption{}
	for rows.Next() {
		var r Redemption
		if err := rows.Scan(&r.ID, &r.Code, &r.UserID, &r.Username, &r.Email, &r.TransactionID,
			&r.DiscountAmount, &r.TokensGranted, &r.OriginalAmount, &r.FinalAmount,
			&r.Currency, &r.RedeemedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRedemptionsForUser powers the "your coupons" list on the redeem page.
func (s *Store) ListRedemptionsForUser(ctx context.Context, userID string, limit int) ([]Redemption, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, code, user_id, username, email, transaction_id, discount_amount,
		       tokens_granted, original_amount, final_amount, currency, redeemed_at
		FROM coupon_redemptions WHERE user_id = $1 ORDER BY redeemed_at DESC LIMIT $2`,
		userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Redemption{}
	for rows.Next() {
		var r Redemption
		if err := rows.Scan(&r.ID, &r.Code, &r.UserID, &r.Username, &r.Email, &r.TransactionID,
			&r.DiscountAmount, &r.TokensGranted, &r.OriginalAmount, &r.FinalAmount,
			&r.Currency, &r.RedeemedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── Update ───────────────────────────────────────────────────────────────────

func (s *Store) SetActive(ctx context.Context, code string, active bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE coupons SET is_active = $1, updated_at = now() WHERE code = $2`,
		active, NormalizeCode(code))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetBatchActive switches a whole generated batch on or off at once —
// the "that campaign leaked, kill it" button.
func (s *Store) SetBatchActive(ctx context.Context, batchID string, active bool) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE coupons SET is_active = $1, updated_at = now() WHERE batch_id = $2`,
		active, batchID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Delete removes a coupon that has never been redeemed. Redeemed coupons
// are refused rather than cascaded: coupon_redemptions is financial
// history, and deleting the parent row would erase the record of a
// discount that was actually given. Switch it off instead.
func (s *Store) Delete(ctx context.Context, code string) error {
	code = NormalizeCode(code)
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM coupon_redemptions WHERE code = $1`, code).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("this coupon has been used %d times, so it can't be deleted — switch it off instead", n)
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM coupons WHERE code = $1`, code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Evaluation (read-only preview) ───────────────────────────────────────────

// checkUsable runs every condition that does not depend on the order
// itself. Shared by Evaluate and Redeem so the preview and the real thing
// can never disagree about what "valid" means.
func checkUsable(c *Coupon, now time.Time) error {
	if !c.IsActive {
		return ErrInactive
	}
	if now.Before(c.StartsAt) {
		return ErrNotStarted
	}
	if c.ExpiresAt != nil && now.After(*c.ExpiresAt) {
		return ErrExpired
	}
	if c.MaxRedemptions != nil && c.RedeemedCount >= *c.MaxRedemptions {
		return ErrExhausted
	}
	return nil
}

func checkOrder(c *Coupon, planID string, amount float64) error {
	if len(c.AppliesToPlans) > 0 && planID != "" {
		ok := false
		for _, p := range c.AppliesToPlans {
			if strings.EqualFold(strings.TrimSpace(p), planID) {
				ok = true
				break
			}
		}
		if !ok {
			return ErrPlanNotAllowed
		}
	}
	if amount < c.MinAmount {
		return ErrBelowMinimum
	}
	return nil
}

// ComputeDiscount works out what a coupon takes off an order. Pure, so it
// is directly unit-testable and is the same arithmetic in preview and in
// redemption.
func ComputeDiscount(c *Coupon, amount float64) (discount float64, tokens int) {
	switch c.Kind {
	case KindPercent:
		discount = amount * c.Value / 100
		if c.MaxDiscount != nil && discount > *c.MaxDiscount {
			discount = *c.MaxDiscount
		}
	case KindFlat:
		discount = c.Value
	case KindTokens:
		return 0, int(c.Value)
	}
	// A coupon can take an order to zero but never below it — a negative
	// total would be a refund, which is not what a discount code is for.
	if discount > amount {
		discount = amount
	}
	if discount < 0 {
		discount = 0
	}
	return roundCents(discount), 0
}

func roundCents(v float64) float64 {
	if v < 0 {
		return -float64(int64(-v*100+0.5)) / 100
	}
	return float64(int64(v*100+0.5)) / 100
}

// Describe renders a coupon as a phrase for the UI.
func Describe(c *Coupon) string {
	switch c.Kind {
	case KindPercent:
		s := fmt.Sprintf("%g%% off", c.Value)
		if c.MaxDiscount != nil {
			s += fmt.Sprintf(", up to %s%.2f", currencySymbol(c.Currency), *c.MaxDiscount)
		}
		return s
	case KindFlat:
		return fmt.Sprintf("%s%.2f off", currencySymbol(c.Currency), c.Value)
	case KindTokens:
		return fmt.Sprintf("%d free fields", int(c.Value))
	}
	return ""
}

func currencySymbol(cur string) string {
	switch strings.ToUpper(cur) {
	case "USD":
		return "$"
	case "INR":
		return "₹"
	case "EUR":
		return "€"
	case "GBP":
		return "£"
	default:
		return cur + " "
	}
}

// Evaluate previews a coupon against an order without consuming it.
//
// Safe to call on every keystroke: it takes no locks and writes nothing.
// The tradeoff is that its answer can be stale by the time the user
// clicks pay — which is exactly why Redeem re-checks everything rather
// than trusting this result.
func (s *Store) Evaluate(ctx context.Context, code, userID, planID string, amount float64) (*Evaluation, error) {
	c, err := s.Get(ctx, code)
	if err != nil {
		return nil, err
	}
	eval := &Evaluation{
		Code:           c.Code,
		Kind:           c.Kind,
		OriginalAmount: roundCents(amount),
		FinalAmount:    roundCents(amount),
		Currency:       c.Currency,
		Description:    Describe(c),
	}

	if err := checkUsable(c, time.Now()); err != nil {
		eval.Reason = err.Error()
		return eval, err
	}
	// Token coupons are not tied to an order, so plan and minimum-amount
	// checks do not apply to them.
	if c.Kind != KindTokens {
		if err := checkOrder(c, planID, amount); err != nil {
			eval.Reason = err.Error()
			return eval, err
		}
	}
	if userID != "" {
		used, err := s.CountUserRedemptions(ctx, c.Code, userID)
		if err != nil {
			return nil, err
		}
		if used >= c.MaxPerUser {
			eval.Reason = ErrAlreadyUsed.Error()
			return eval, ErrAlreadyUsed
		}
	}

	discount, tokens := ComputeDiscount(c, amount)
	eval.Valid = true
	eval.DiscountAmount = discount
	eval.TokensGranted = tokens
	eval.FinalAmount = roundCents(amount - discount)
	return eval, nil
}

// ── Redemption (atomic) ──────────────────────────────────────────────────────

// RedeemParams describes one redemption attempt.
type RedeemParams struct {
	Code          string
	UserID        string
	Username      string
	Email         string
	PlanID        string
	Amount        float64 // order total before discount; 0 for token coupons
	Currency      string
	TransactionID string // plan_transactions.id, empty for token grants
}

// Redeem consumes one redemption, atomically.
//
// Everything below happens inside one transaction, and the coupon row is
// locked FOR UPDATE before any check runs. That ordering is the whole
// point: checks performed outside the lock would be answering a question
// about a row another transaction is simultaneously changing.
//
// The caller is responsible for what happens with the result — crediting
// tokens for a token coupon, or passing the discounted amount to the
// payment gateway. Redeem does not touch users.token_balance itself,
// because doing so would make it impossible to redeem a coupon and charge
// a card in one consistent unit of work at the call site.
func (s *Store) Redeem(ctx context.Context, p RedeemParams) (*Redemption, *Coupon, error) {
	code := NormalizeCode(p.Code)
	if code == "" {
		return nil, nil, ErrNotFound
	}
	if p.UserID == "" {
		return nil, nil, errors.New("a coupon can only be redeemed by a signed-in user")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Rollback after a successful Commit is a no-op, so this deferred
	// call is safe on every path and guarantees no connection is left
	// holding a lock if an early return happens.
	defer func() { _ = tx.Rollback(ctx) }()

	c, err := scanCoupon(tx.QueryRow(ctx,
		`SELECT `+couponColumns+` FROM coupons WHERE code = $1 FOR UPDATE`, code))
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	if err := checkUsable(c, now); err != nil {
		return nil, c, err
	}
	if c.Kind != KindTokens {
		if err := checkOrder(c, p.PlanID, p.Amount); err != nil {
			return nil, c, err
		}
	}

	// Per-user limit, counted inside the transaction. Outside it, two
	// simultaneous requests from the same user would both read zero.
	var used int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM coupon_redemptions WHERE code = $1 AND user_id = $2`,
		code, p.UserID).Scan(&used); err != nil {
		return nil, c, err
	}
	if used >= c.MaxPerUser {
		return nil, c, ErrAlreadyUsed
	}

	discount, tokens := ComputeDiscount(c, p.Amount)
	final := roundCents(p.Amount - discount)

	if _, err := tx.Exec(ctx,
		`UPDATE coupons SET redeemed_count = redeemed_count + 1, updated_at = now() WHERE code = $1`,
		code); err != nil {
		return nil, c, err
	}

	currency := p.Currency
	if currency == "" {
		currency = c.Currency
	}
	r := &Redemption{
		ID:             newID(),
		Code:           code,
		UserID:         p.UserID,
		DiscountAmount: discount,
		TokensGranted:  tokens,
		OriginalAmount: roundCents(p.Amount),
		FinalAmount:    final,
		Currency:       currency,
		RedeemedAt:     now,
	}
	if p.Username != "" {
		r.Username = &p.Username
	}
	if p.Email != "" {
		r.Email = &p.Email
	}
	if p.TransactionID != "" {
		r.TransactionID = &p.TransactionID
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO coupon_redemptions
		 (id, code, user_id, username, email, transaction_id, discount_amount,
		  tokens_granted, original_amount, final_amount, currency, redeemed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, now())`,
		r.ID, r.Code, r.UserID, r.Username, r.Email, r.TransactionID,
		r.DiscountAmount, r.TokensGranted, r.OriginalAmount, r.FinalAmount, r.Currency,
	); err != nil {
		return nil, c, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, c, err
	}
	c.RedeemedCount++
	return r, c, nil
}

// ReleaseRedemption undoes a redemption, giving the use back.
//
// Needed because a discount coupon has to be consumed BEFORE the card is
// charged — consuming it after would let two simultaneous purchases both
// pass the "one use left" check and both get the discount. Reserving
// first makes over-redemption impossible, and this is the compensating
// action for the case where the charge then fails: the row is removed and
// redeemed_count is decremented, so a declined card does not silently eat
// the customer's coupon.
//
// Both statements run in one transaction, so the counter and the
// redemption row can never disagree. Decrementing is guarded with
// GREATEST(...,0) so a double release cannot drive the count negative and
// hand out unlimited redemptions.
func (s *Store) ReleaseRedemption(ctx context.Context, redemptionID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var code string
	if err := tx.QueryRow(ctx,
		`DELETE FROM coupon_redemptions WHERE id = $1 RETURNING code`, redemptionID).Scan(&code); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already released, or never recorded. Nothing to undo.
			return nil
		}
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE coupons SET redeemed_count = GREATEST(redeemed_count - 1, 0), updated_at = now()
		 WHERE code = $1`, code); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AttachTransaction links a redemption to the transaction it discounted,
// once the transaction id is known.
func (s *Store) AttachTransaction(ctx context.Context, redemptionID, transactionID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE coupon_redemptions SET transaction_id = $2 WHERE id = $1`,
		redemptionID, transactionID)
	return err
}

// ── Stats ────────────────────────────────────────────────────────────────────

type Stats struct {
	TotalCoupons     int     `json:"totalCoupons"`
	ActiveCoupons    int     `json:"activeCoupons"`
	TotalRedemptions int     `json:"totalRedemptions"`
	TotalDiscount    float64 `json:"totalDiscount"`
	TotalTokens      int     `json:"totalTokens"`
}

func (s *Store) Stats(ctx context.Context) (*Stats, error) {
	var st Stats
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE is_active AND (expires_at IS NULL OR expires_at > now()))
		FROM coupons`).Scan(&st.TotalCoupons, &st.ActiveCoupons); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*), coalesce(sum(discount_amount),0)::float8, coalesce(sum(tokens_granted),0)
		FROM coupon_redemptions`).
		Scan(&st.TotalRedemptions, &st.TotalDiscount, &st.TotalTokens); err != nil {
		return nil, err
	}
	return &st, nil
}

// ── ids ──────────────────────────────────────────────────────────────────────

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("cr_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("cr_%x", b)
}

// RedemptionIDForTransaction finds the redemption attached to a
// transaction, if there is one. Returns "" when there is none.
func (s *Store) RedemptionIDForTransaction(ctx context.Context, transactionID string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM coupon_redemptions WHERE transaction_id = $1 LIMIT 1`,
		transactionID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}
