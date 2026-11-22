// internal/alerting/engine.go
//
// The evaluation loop: read rules, compute metrics, decide what counts as
// a problem, and email the right people.
//
// THE STATE MACHINE
//
//	        breaching for >= ForSeconds
//	OK ──────────────────────────────────▶ FIRING ──┐
//	 ▲                                        │     │ still breaching and
//	 │         metric back under threshold    │     │ cooldown elapsed
//	 └────────────────────────────────────────┘     └─▶ re-notify
//
// Three parameters shape every rule, and each exists to suppress a
// specific kind of useless email:
//
//	WindowSeconds   — how far back the metric looks. Too short and one
//	                  slow request is a 100% error rate.
//	ForSeconds      — how long the breach must persist before anyone is
//	                  told. This is what separates a blip from an outage.
//	CooldownSeconds — minimum gap between repeat emails while the problem
//	                  continues. Without it, a rule evaluated every 30
//	                  seconds sends 120 emails an hour and everyone
//	                  creates a filter that deletes them.
//
// A resolved alert always emails, ignoring cooldown — "it's fixed" is
// never noise, and an unresolved-looking incident is expensive.
package alerting

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	imetrics "hitl-platform/internal/metrics"
)

// Notifier delivers an alert. Implemented by EmailNotifier in notify.go;
// an interface so tests can assert on what would have been sent, and so a
// Slack or PagerDuty sink can be added without touching the engine.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
	Name() string
}

// Notification is one rendered alert ready to deliver.
type Notification struct {
	Recipients []string
	Rule       Rule
	Event      Event
	Resolved   bool
	Observed   float64
	TraceID    string
}

// Engine evaluates rules on a ticker.
type Engine struct {
	store    *Store
	pool     *pgxpool.Pool
	notifier Notifier
	interval time.Duration

	mu    sync.Mutex
	state map[string]*ruleState // keyed by rule id
}

type ruleState struct {
	breachingSince time.Time
	firing         bool
	eventID        string
	lastNotified   time.Time
}

// NewEngine builds an engine. interval defaults to 30s.
func NewEngine(store *Store, pool *pgxpool.Pool, notifier Notifier, interval time.Duration) *Engine {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Engine{
		store:    store,
		pool:     pool,
		notifier: notifier,
		interval: interval,
		state:    map[string]*ruleState{},
	}
}

// Start runs the loop until ctx is cancelled.
func (e *Engine) Start(ctx context.Context) {
	// Restore what was firing before the restart, so an ongoing incident
	// is continued rather than re-announced.
	if recovered, err := e.store.RecoverFiringEvents(ctx); err == nil {
		e.mu.Lock()
		for ruleID, ev := range recovered {
			st := &ruleState{
				firing:         true,
				eventID:        ev.ID,
				breachingSince: ev.StartedAt,
			}
			if ev.LastNotifiedAt != nil {
				st.lastNotified = *ev.LastNotifiedAt
			}
			e.state[ruleID] = st
		}
		n := len(recovered)
		e.mu.Unlock()
		if n > 0 {
			imetrics.Log("info", "alerting_recovered_state", map[string]any{
				"firing_alerts": n,
			})
		}
	} else {
		imetrics.Log("warn", "alerting_recover_failed", map[string]any{"error": err.Error()})
	}

	imetrics.Log("info", "alerting_started", map[string]any{
		"interval_seconds": e.interval.Seconds(),
		"notifier":         e.notifier.Name(),
	})

	go func() {
		t := time.NewTicker(e.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				imetrics.Log("info", "alerting_stopped", nil)
				return
			case <-t.C:
				e.EvaluateAll(ctx)
			}
		}
	}()
}

// EvaluateAll runs one full pass. Exported so an admin endpoint can force
// an immediate evaluation instead of waiting for the next tick.
func (e *Engine) EvaluateAll(ctx context.Context) {
	start := time.Now()
	defer func() {
		imetrics.AlertEvaluationDuration.With().Observe(time.Since(start).Seconds())
	}()

	rules, err := e.store.ListRules(ctx, true)
	if err != nil {
		imetrics.Log("error", "alerting_rule_load_failed", map[string]any{"error": err.Error()})
		return
	}
	imetrics.AlertRulesActive.With().Set(float64(len(rules)))

	firingBySeverity := map[string]float64{
		SeverityInfo: 0, SeverityWarning: 0, SeverityCritical: 0,
	}

	for i := range rules {
		rule := rules[i]
		// One rule's failure must not stop the others — a rule with a bad
		// label would otherwise silently disable every rule after it in
		// the list.
		func() {
			defer func() {
				if r := recover(); r != nil {
					imetrics.Log("error", "alerting_rule_panicked", map[string]any{
						"rule":  rule.Name,
						"panic": fmt.Sprint(r),
					})
					imetrics.AlertEvaluationErrors.With(rule.Name, rule.Metric).Inc()
				}
			}()
			e.evaluateRule(ctx, rule)
		}()

		e.mu.Lock()
		if st, ok := e.state[rule.ID]; ok && st.firing {
			firingBySeverity[rule.Severity]++
		}
		e.mu.Unlock()
	}

	for sev, n := range firingBySeverity {
		imetrics.AlertsFiring.With(sev).Set(n)
	}
}

// evaluateRule advances one rule's state machine by one tick.
func (e *Engine) evaluateRule(ctx context.Context, rule Rule) {
	resolver, ok := Catalog[rule.Metric]
	if !ok {
		imetrics.AlertEvaluationErrors.With(rule.Name, rule.Metric).Inc()
		imetrics.Log("error", "alerting_unknown_metric", map[string]any{
			"rule": rule.Name, "metric": rule.Metric,
		})
		return
	}

	// Bound each resolver so one slow query cannot stall the whole pass.
	evalCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	traceID := imetrics.NewTraceID()
	evalCtx = imetrics.WithTraceID(evalCtx, traceID)
	defer cancel()

	value, err := resolver.Fn(ResolveContext{
		Ctx:    evalCtx,
		Pool:   e.pool,
		Window: rule.Window(),
		Labels: rule.Labels,
	})
	if err != nil {
		imetrics.AlertEvaluations.With(rule.Name, "error").Inc()
		imetrics.AlertEvaluationErrors.With(rule.Name, rule.Metric).Inc()
		imetrics.Log("error", "alerting_evaluation_failed", map[string]any{
			"rule": rule.Name, "metric": rule.Metric,
			"error": err.Error(), "trace_id": traceID,
		})
		// An unevaluable rule is left in whatever state it was in. It is
		// neither "fine" nor "broken" — pretending the metric is healthy
		// would resolve a real alert on a transient database hiccup.
		return
	}

	breaching := Compare(value, rule.Comparator, rule.Threshold)
	if breaching {
		imetrics.AlertEvaluations.With(rule.Name, "breaching").Inc()
	} else {
		imetrics.AlertEvaluations.With(rule.Name, "ok").Inc()
	}

	e.mu.Lock()
	st, ok := e.state[rule.ID]
	if !ok {
		st = &ruleState{}
		e.state[rule.ID] = st
	}
	act, next, eventID := decideTransition(*st, breaching, time.Now(), rule)
	*st = next
	e.mu.Unlock()

	switch act {
	case actionFire:
		e.fire(ctx, rule, value, eventID, traceID)
	case actionRenotify:
		e.renotify(ctx, rule, value, eventID, traceID)
	case actionResolve:
		e.resolve(ctx, rule, value, eventID, traceID)
	}
}

// ── The state machine, as a pure function ────────────────────────────────────

type action int

const (
	actionNone action = iota
	actionFire
	actionRenotify
	actionResolve
)

func (a action) String() string {
	switch a {
	case actionFire:
		return "fire"
	case actionRenotify:
		return "renotify"
	case actionResolve:
		return "resolve"
	default:
		return "none"
	}
}

// decideTransition advances a rule's state by one evaluation and reports
// what should be done about it.
//
// Pure — no clock, no locks, no I/O — so every path through it is
// directly testable, which matters because the failure modes here are
// subtle and only show up in production: an alert that never fires, an
// alert that fires every 30 seconds forever, an alert that resolves
// itself while the problem is ongoing.
// Returns the action to take, the rule's next state, and the id of the
// alert event the action refers to.
func decideTransition(st ruleState, breaching bool, now time.Time, rule Rule) (action, ruleState, string) {
	forDur := time.Duration(rule.ForSeconds) * time.Second
	cooldown := time.Duration(rule.CooldownSeconds) * time.Second

	if !breaching {
		// Recovered. Clear the timer either way; only emit a resolution
		// if there was something to resolve.
		st.breachingSince = time.Time{}
		if st.firing {
			closing := st.eventID
			st.firing = false
			st.eventID = ""
			return actionResolve, st, closing
		}
		return actionNone, st, ""
	}

	if st.breachingSince.IsZero() {
		st.breachingSince = now
	}

	if !st.firing {
		if now.Sub(st.breachingSince) >= forDur {
			st.firing = true
			st.eventID = newID("alert")
			st.lastNotified = now
			return actionFire, st, st.eventID
		}
		// Breaching, but not for long enough yet. Say nothing.
		return actionNone, st, ""
	}

	// Already firing. Repeat only once the cooldown has elapsed.
	if now.Sub(st.lastNotified) >= cooldown {
		st.lastNotified = now
		return actionRenotify, st, st.eventID
	}
	return actionNone, st, ""
}

func (e *Engine) fire(ctx context.Context, rule Rule, value float64, eventID, traceID string) {
	msg := describeBreach(rule, value)
	ev := Event{
		ID:            eventID,
		RuleID:        &rule.ID,
		RuleName:      rule.Name,
		Metric:        rule.Metric,
		Severity:      rule.Severity,
		Comparator:    rule.Comparator,
		Threshold:     rule.Threshold,
		ObservedValue: value,
		State:         "firing",
		Message:       &msg,
		TraceID:       &traceID,
		StartedAt:     time.Now(),
	}
	if err := e.store.InsertEvent(ctx, &ev); err != nil {
		imetrics.Log("error", "alerting_event_write_failed", map[string]any{
			"rule": rule.Name, "error": err.Error(),
		})
	}

	imetrics.Log("error", "alert_fired", map[string]any{
		"rule": rule.Name, "metric": rule.Metric, "severity": rule.Severity,
		"observed": value, "threshold": rule.Threshold, "trace_id": traceID,
		"alert_id": eventID,
	})

	e.dispatch(ctx, rule, ev, false, value, traceID)
}

func (e *Engine) renotify(ctx context.Context, rule Rule, value float64, eventID, traceID string) {
	msg := describeBreach(rule, value)
	ev := Event{
		ID: eventID, RuleID: &rule.ID, RuleName: rule.Name, Metric: rule.Metric,
		Severity: rule.Severity, Comparator: rule.Comparator, Threshold: rule.Threshold,
		ObservedValue: value, State: "firing", Message: &msg, TraceID: &traceID,
	}
	imetrics.Log("warn", "alert_still_firing", map[string]any{
		"rule": rule.Name, "observed": value, "alert_id": eventID, "trace_id": traceID,
	})
	e.dispatch(ctx, rule, ev, false, value, traceID)
}

func (e *Engine) resolve(ctx context.Context, rule Rule, value float64, eventID, traceID string) {
	if eventID != "" {
		if err := e.store.ResolveEvent(ctx, eventID, value); err != nil {
			imetrics.Log("error", "alerting_resolve_write_failed", map[string]any{
				"rule": rule.Name, "error": err.Error(),
			})
		}
	}
	msg := fmt.Sprintf("%s is back to normal: %s is now %s (threshold %s).",
		rule.Name, rule.Metric, formatValue(rule, value), formatThreshold(rule))

	ev := Event{
		ID: eventID, RuleID: &rule.ID, RuleName: rule.Name, Metric: rule.Metric,
		Severity: rule.Severity, Comparator: rule.Comparator, Threshold: rule.Threshold,
		ObservedValue: value, State: "resolved", Message: &msg, TraceID: &traceID,
	}
	imetrics.Log("info", "alert_resolved", map[string]any{
		"rule": rule.Name, "observed": value, "alert_id": eventID, "trace_id": traceID,
	})
	e.dispatch(ctx, rule, ev, true, value, traceID)
}

// dispatch resolves recipients and hands the notification to the notifier.
func (e *Engine) dispatch(ctx context.Context, rule Rule, ev Event, resolved bool, value float64, traceID string) {
	recipients, err := e.store.RecipientsFor(ctx, rule.ID, rule.Severity)
	if err != nil {
		imetrics.AlertNotifications.With(rule.Severity, "failed").Inc()
		imetrics.Log("error", "alerting_recipient_lookup_failed", map[string]any{
			"rule": rule.Name, "error": err.Error(),
		})
		return
	}
	if len(recipients) == 0 {
		// Worth surfacing loudly. A rule with no recipients looks like it
		// is working right up until the moment it matters.
		imetrics.AlertNotifications.With(rule.Severity, "no_recipients").Inc()
		imetrics.Log("warn", "alert_has_no_recipients", map[string]any{
			"rule":     rule.Name,
			"severity": rule.Severity,
			"hint":     "add an address in the Alerts screen, or POST /api/alerts/recipients",
		})
		return
	}

	notifyErr := e.notifier.Notify(ctx, Notification{
		Recipients: recipients, Rule: rule, Event: ev,
		Resolved: resolved, Observed: value, TraceID: traceID,
	})
	if notifyErr != nil {
		imetrics.AlertNotifications.With(rule.Severity, "failed").Inc()
		imetrics.Log("error", "alert_notify_failed", map[string]any{
			"rule": rule.Name, "error": notifyErr.Error(), "trace_id": traceID,
		})
	} else {
		imetrics.AlertNotifications.With(rule.Severity, "sent").Inc()
	}

	if ev.ID != "" {
		if err := e.store.MarkNotified(ctx, ev.ID, notifyErr); err != nil {
			imetrics.Log("warn", "alerting_notify_record_failed", map[string]any{
				"rule": rule.Name, "error": err.Error(),
			})
		}
	}
}

// ── Formatting ───────────────────────────────────────────────────────────────

func describeBreach(rule Rule, value float64) string {
	return fmt.Sprintf("%s %s the threshold: %s is %s, threshold is %s.",
		rule.Name, ComparatorPhrase(rule.Comparator), rule.Metric,
		formatValue(rule, value), formatThreshold(rule))
}

// formatValue renders a number with the unit its resolver declared, so an
// email says "2,431 ms" rather than "2431".
func formatValue(rule Rule, v float64) string {
	unit := ""
	if r, ok := Catalog[rule.Metric]; ok {
		unit = r.Unit
	}
	switch unit {
	case "percent":
		return fmt.Sprintf("%.1f%%", v)
	case "ms":
		return fmt.Sprintf("%.0f ms", v)
	case "seconds":
		return fmt.Sprintf("%.1f s", v)
	case "minutes":
		return fmt.Sprintf("%.0f min", v)
	case "per_min":
		return fmt.Sprintf("%.1f/min", v)
	case "ratio":
		return fmt.Sprintf("%.2f", v)
	case "bool":
		if v >= 1 {
			return "reachable"
		}
		return "unreachable"
	default:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%.2f", v)
	}
}

func formatThreshold(rule Rule) string {
	return formatValue(rule, rule.Threshold)
}

// ── Manual test path ─────────────────────────────────────────────────────────

// SendTest delivers a sample alert so an operator can confirm mail
// actually arrives, without waiting for something to break.
//
// This is the single most valuable endpoint in the whole subsystem. Email
// configuration fails quietly and constantly — an unverified SendGrid
// sender, a wrong SMTP port, a spam filter — and the default way to find
// out is during an incident, when the alert that would have helped never
// arrived.
func (e *Engine) SendTest(ctx context.Context, to string) error {
	rule := Rule{
		ID: "test", Name: "Test alert", Metric: "http_error_rate_pct",
		Comparator: "gt", Threshold: 5, Severity: SeverityInfo,
		WindowSeconds: 300,
	}
	msg := "This is a test alert. If you're reading it, alert email is configured correctly."
	ev := Event{
		ID: "test", RuleName: rule.Name, Metric: rule.Metric, Severity: rule.Severity,
		Comparator: rule.Comparator, Threshold: rule.Threshold, ObservedValue: 42,
		State: "firing", Message: &msg, StartedAt: time.Now(),
	}

	recipients := []string{}
	if strings.TrimSpace(to) != "" {
		recipients = append(recipients, strings.TrimSpace(to))
	} else {
		found, err := e.store.RecipientsFor(ctx, "", SeverityInfo)
		if err != nil {
			return err
		}
		recipients = found
	}
	if len(recipients) == 0 {
		return fmt.Errorf("no recipients configured — add an address first")
	}
	return e.notifier.Notify(ctx, Notification{
		Recipients: recipients, Rule: rule, Event: ev, Observed: 42,
		TraceID: imetrics.NewTraceID(),
	})
}

// FiringSnapshot reports which rules are currently firing, for the UI.
func (e *Engine) FiringSnapshot() map[string]time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]time.Time{}
	for ruleID, st := range e.state {
		if st.firing {
			out[ruleID] = st.breachingSince
		}
	}
	return out
}
