// internal/alerting/store.go
//
// Persistence for alert rules, email recipients, and fired-alert history.
package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("no such alert rule")

// ── Severity ─────────────────────────────────────────────────────────────────

const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// severityRank orders severities so a recipient's severity_min can be
// compared against an alert's severity.
func severityRank(s string) int {
	switch strings.ToLower(s) {
	case SeverityInfo:
		return 0
	case SeverityWarning:
		return 1
	case SeverityCritical:
		return 2
	default:
		return 1
	}
}

func ValidSeverity(s string) bool {
	switch strings.ToLower(s) {
	case SeverityInfo, SeverityWarning, SeverityCritical:
		return true
	}
	return false
}

// ── Comparators ──────────────────────────────────────────────────────────────

func ValidComparator(c string) bool {
	switch c {
	case "gt", "gte", "lt", "lte", "eq":
		return true
	}
	return false
}

// Compare reports whether value breaches threshold under comparator.
func Compare(value float64, comparator string, threshold float64) bool {
	switch comparator {
	case "gt":
		return value > threshold
	case "gte":
		return value >= threshold
	case "lt":
		return value < threshold
	case "lte":
		return value <= threshold
	case "eq":
		return value == threshold
	}
	return false
}

// ComparatorPhrase renders a comparator for an email subject line.
func ComparatorPhrase(c string) string {
	switch c {
	case "gt":
		return "is above"
	case "gte":
		return "is at or above"
	case "lt":
		return "has fallen below"
	case "lte":
		return "is at or below"
	case "eq":
		return "equals"
	}
	return "breached"
}

// ── Models ───────────────────────────────────────────────────────────────────

type Rule struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Description     *string           `json:"description,omitempty"`
	Metric          string            `json:"metric"`
	Comparator      string            `json:"comparator"`
	Threshold       float64           `json:"threshold"`
	Severity        string            `json:"severity"`
	WindowSeconds   int               `json:"windowSeconds"`
	ForSeconds      int               `json:"forSeconds"`
	CooldownSeconds int               `json:"cooldownSeconds"`
	Labels          map[string]string `json:"labels,omitempty"`
	IsActive        bool              `json:"isActive"`
	CreatedBy       *string           `json:"createdBy,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
}

// Validate normalizes and checks a rule. Messages are user-facing.
func (r *Rule) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return errors.New("give the rule a name")
	}
	if _, ok := Catalog[r.Metric]; !ok {
		return fmt.Errorf("unknown metric %q — pick one from the catalog", r.Metric)
	}
	if r.Comparator == "" {
		r.Comparator = "gt"
	}
	if !ValidComparator(r.Comparator) {
		return fmt.Errorf("unknown comparator %q — use gt, gte, lt, lte or eq", r.Comparator)
	}
	if r.Severity == "" {
		r.Severity = SeverityWarning
	}
	if !ValidSeverity(r.Severity) {
		return fmt.Errorf("unknown severity %q — use info, warning or critical", r.Severity)
	}
	if r.WindowSeconds <= 0 {
		r.WindowSeconds = 300
	}
	if r.ForSeconds < 0 {
		r.ForSeconds = 0
	}
	if r.CooldownSeconds <= 0 {
		// Without a cooldown a rule that stays breached re-notifies on
		// every evaluation tick — a few hundred emails an hour, after
		// which nobody reads any of them.
		r.CooldownSeconds = 1800
	}
	return nil
}

func (r *Rule) Window() time.Duration { return time.Duration(r.WindowSeconds) * time.Second }

type Recipient struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Label       *string   `json:"label,omitempty"`
	RuleID      *string   `json:"ruleId,omitempty"`
	SeverityMin string    `json:"severityMin"`
	IsActive    bool      `json:"isActive"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Event struct {
	ID             string     `json:"id"`
	RuleID         *string    `json:"ruleId,omitempty"`
	RuleName       string     `json:"ruleName"`
	Metric         string     `json:"metric"`
	Severity       string     `json:"severity"`
	Comparator     string     `json:"comparator"`
	Threshold      float64    `json:"threshold"`
	ObservedValue  float64    `json:"observedValue"`
	State          string     `json:"state"`
	Message        *string    `json:"message,omitempty"`
	TraceID        *string    `json:"traceId,omitempty"`
	NotifyCount    int        `json:"notifyCount"`
	NotifyError    *string    `json:"notifyError,omitempty"`
	StartedAt      time.Time  `json:"startedAt"`
	LastNotifiedAt *time.Time `json:"lastNotifiedAt,omitempty"`
	ResolvedAt     *time.Time `json:"resolvedAt,omitempty"`
}

// ── Store ────────────────────────────────────────────────────────────────────

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const ruleColumns = `id, name, description, metric, comparator, threshold, severity,
	window_seconds, for_seconds, cooldown_seconds, labels, is_active, created_by, created_at, updated_at`

func scanRule(row pgx.Row) (*Rule, error) {
	var r Rule
	var labels *string
	err := row.Scan(&r.ID, &r.Name, &r.Description, &r.Metric, &r.Comparator, &r.Threshold,
		&r.Severity, &r.WindowSeconds, &r.ForSeconds, &r.CooldownSeconds, &labels,
		&r.IsActive, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Labels = parseLabels(labels)
	return &r, nil
}

// parseLabels tolerates malformed JSON rather than failing the whole
// rule load. A rule with unreadable labels still evaluates (resolvers
// that need a label will report a clear error); a rule that fails to load
// silently stops alerting, which is the worse outcome.
func parseLabels(s *string) map[string]string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(*s), &m); err != nil {
		return nil
	}
	return m
}

func encodeLabels(m map[string]string) *string {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

func (s *Store) ListRules(ctx context.Context, activeOnly bool) ([]Rule, error) {
	q := `SELECT ` + ruleColumns + ` FROM alert_rules`
	if activeOnly {
		q += ` WHERE is_active = true`
	}
	q += ` ORDER BY severity DESC, name ASC`

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) GetRule(ctx context.Context, id string) (*Rule, error) {
	return scanRule(s.pool.QueryRow(ctx, `SELECT `+ruleColumns+` FROM alert_rules WHERE id = $1`, id))
}

func (s *Store) CreateRule(ctx context.Context, r *Rule) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.ID == "" {
		r.ID = newID("rule")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO alert_rules (`+ruleColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13, now(), now())`,
		r.ID, r.Name, r.Description, r.Metric, r.Comparator, r.Threshold, r.Severity,
		r.WindowSeconds, r.ForSeconds, r.CooldownSeconds, encodeLabels(r.Labels),
		r.IsActive, r.CreatedBy)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
		return fmt.Errorf("a rule named %q already exists", r.Name)
	}
	return err
}

func (s *Store) UpdateRule(ctx context.Context, r *Rule) error {
	if err := r.Validate(); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE alert_rules SET
		  name = $2, description = $3, metric = $4, comparator = $5, threshold = $6,
		  severity = $7, window_seconds = $8, for_seconds = $9, cooldown_seconds = $10,
		  labels = $11, is_active = $12, updated_at = now()
		WHERE id = $1`,
		r.ID, r.Name, r.Description, r.Metric, r.Comparator, r.Threshold, r.Severity,
		r.WindowSeconds, r.ForSeconds, r.CooldownSeconds, encodeLabels(r.Labels), r.IsActive)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteRule(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM alert_rules WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Recipients ───────────────────────────────────────────────────────────────

func (s *Store) ListRecipients(ctx context.Context) ([]Recipient, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, email, label, rule_id, severity_min, is_active, created_at
		FROM alert_recipients ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Recipient{}
	for rows.Next() {
		var r Recipient
		if err := rows.Scan(&r.ID, &r.Email, &r.Label, &r.RuleID, &r.SeverityMin,
			&r.IsActive, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecipientsFor returns the active addresses that should hear about this
// alert: those scoped to this rule, plus the global ones, filtered by
// each recipient's minimum severity.
//
// Deduplicated by address, because one person subscribed both globally
// and to this specific rule should get one email, not two.
func (s *Store) RecipientsFor(ctx context.Context, ruleID, severity string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT email, severity_min FROM alert_recipients
		WHERE is_active = true AND (rule_id = $1 OR rule_id IS NULL)`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rank := severityRank(severity)
	seen := map[string]bool{}
	out := []string{}
	for rows.Next() {
		var email, min string
		if err := rows.Scan(&email, &min); err != nil {
			return nil, err
		}
		if severityRank(min) > rank {
			continue
		}
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" || seen[email] {
			continue
		}
		seen[email] = true
		out = append(out, email)
	}
	return out, rows.Err()
}

func (s *Store) AddRecipient(ctx context.Context, r *Recipient) error {
	r.Email = strings.ToLower(strings.TrimSpace(r.Email))
	if !looksLikeEmail(r.Email) {
		return fmt.Errorf("%q doesn't look like an email address", r.Email)
	}
	if r.SeverityMin == "" {
		r.SeverityMin = SeverityWarning
	}
	if !ValidSeverity(r.SeverityMin) {
		return fmt.Errorf("unknown severity %q — use info, warning or critical", r.SeverityMin)
	}
	if r.ID == "" {
		r.ID = newID("rcpt")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO alert_recipients (id, email, label, rule_id, severity_min, is_active, created_at)
		VALUES ($1,$2,$3,$4,$5,$6, now())`,
		r.ID, r.Email, r.Label, r.RuleID, r.SeverityMin, r.IsActive)
	return err
}

func (s *Store) SetRecipientActive(ctx context.Context, id string, active bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE alert_recipients SET is_active = $2 WHERE id = $1`, id, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("no such recipient")
	}
	return nil
}

func (s *Store) DeleteRecipient(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM alert_recipients WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("no such recipient")
	}
	return nil
}

// looksLikeEmail is a deliberately loose check. Full RFC 5322 validation
// rejects addresses that work and accepts addresses that do not; the only
// real test is whether mail is delivered. This catches typing a name into
// the wrong box, which is the mistake that actually happens here.
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	domain := s[at+1:]
	return strings.Contains(domain, ".") && !strings.ContainsAny(s, " \t\n,;")
}

// ── Events ───────────────────────────────────────────────────────────────────

const eventColumns = `id, rule_id, rule_name, metric, severity, comparator, threshold,
	observed_value, state, message, trace_id, notify_count, notify_error,
	started_at, last_notified_at, resolved_at`

func (s *Store) InsertEvent(ctx context.Context, e *Event) error {
	if e.ID == "" {
		e.ID = newID("alert")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO alert_events (`+eventColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13, now(), $14, $15)`,
		e.ID, e.RuleID, e.RuleName, e.Metric, e.Severity, e.Comparator, e.Threshold,
		e.ObservedValue, e.State, e.Message, e.TraceID, e.NotifyCount, e.NotifyError,
		e.LastNotifiedAt, e.ResolvedAt)
	return err
}

func (s *Store) MarkNotified(ctx context.Context, eventID string, notifyErr error) error {
	var errText *string
	if notifyErr != nil {
		t := notifyErr.Error()
		errText = &t
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE alert_events
		SET notify_count = notify_count + 1, last_notified_at = now(), notify_error = $2
		WHERE id = $1`, eventID, errText)
	return err
}

func (s *Store) ResolveEvent(ctx context.Context, eventID string, observed float64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE alert_events
		SET state = 'resolved', resolved_at = now(), observed_value = $2
		WHERE id = $1 AND state = 'firing'`, eventID, observed)
	return err
}

// ListEvents returns alert history, newest first. Pass state="" for all.
func (s *Store) ListEvents(ctx context.Context, state string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + eventColumns + ` FROM alert_events`
	args := []any{}
	if state != "" {
		q += ` WHERE state = $1`
		args = append(args, state)
	}
	args = append(args, limit)
	q += fmt.Sprintf(` ORDER BY started_at DESC LIMIT $%d`, len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.Metric, &e.Severity,
			&e.Comparator, &e.Threshold, &e.ObservedValue, &e.State, &e.Message,
			&e.TraceID, &e.NotifyCount, &e.NotifyError, &e.StartedAt,
			&e.LastNotifiedAt, &e.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RecoverFiringEvents reloads still-firing alerts after a restart.
//
// Without this, restarting the server loses all in-memory alert state, so
// an alert that was already firing would fire again from scratch —
// re-notifying immediately and resetting its cooldown. Deploying during
// an incident would then mail everyone a second time, which is precisely
// when nobody needs more email.
func (s *Store) RecoverFiringEvents(ctx context.Context) (map[string]*Event, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+eventColumns+` FROM alert_events WHERE state = 'firing' AND rule_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]*Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.Metric, &e.Severity,
			&e.Comparator, &e.Threshold, &e.ObservedValue, &e.State, &e.Message,
			&e.TraceID, &e.NotifyCount, &e.NotifyError, &e.StartedAt,
			&e.LastNotifiedAt, &e.ResolvedAt); err != nil {
			return nil, err
		}
		if e.RuleID != nil {
			ev := e
			out[*e.RuleID] = &ev
		}
	}
	return out, rows.Err()
}

func newID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}
