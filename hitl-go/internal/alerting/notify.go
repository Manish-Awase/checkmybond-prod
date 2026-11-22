// internal/alerting/notify.go
//
// Turning an alert into an email someone can act on.
//
// The body is written for the person reading it at 3am on a phone. That
// shapes every choice below: the subject line alone says what broke and
// how badly, the first line of the body says what to do, and the detail
// sits underneath for whoever opens a laptop. No HTML — plain text renders
// identically everywhere, cannot be mangled by a mail client, and never
// gets held back by an image proxy.
package alerting

import (
	"context"
	"fmt"
	"strings"
	"time"

	iemail "hitl-platform/internal/email"
	imetrics "hitl-platform/internal/metrics"
)

// EmailNotifier sends alerts through internal/email, which means it
// honours whatever EMAIL_PROVIDER is already configured for password
// resets and OTPs — SendGrid, SMTP, or stdout in local development.
type EmailNotifier struct {
	// AppName appears in the subject line. Defaults to "CheckMyBond".
	AppName string
	// DashboardURL, when set, is included so the reader can go straight
	// to the graph instead of hunting for it.
	DashboardURL string
}

func (n *EmailNotifier) Name() string { return "email" }

func (n *EmailNotifier) appName() string {
	if n.AppName != "" {
		return n.AppName
	}
	return "CheckMyBond"
}

// Notify delivers one alert to every recipient.
//
// Each address is sent separately and a failure on one does not stop the
// others: if three of four addresses are fine, three people should still
// be told. The combined error is returned so the event row records what
// went wrong.
func (n *EmailNotifier) Notify(ctx context.Context, note Notification) error {
	subject := n.subject(note)
	body := n.body(note)

	var failures []string
	for _, to := range note.Recipients {
		start := time.Now()
		err := iemail.SendAlert(to, subject, body)
		imetrics.EmailDuration.With("alert").Observe(time.Since(start).Seconds())
		if err != nil {
			imetrics.EmailsSent.With("alert", "alert", "failed").Inc()
			failures = append(failures, fmt.Sprintf("%s: %v", to, err))
			continue
		}
		imetrics.EmailsSent.With("alert", "alert", "sent").Inc()
	}

	if len(failures) > 0 {
		return fmt.Errorf("could not deliver to %d of %d recipients — %s",
			len(failures), len(note.Recipients), strings.Join(failures, "; "))
	}
	return nil
}

// subject front-loads the information that decides whether to open it.
//
//	[CheckMyBond CRITICAL] API error rate — 12.4% (threshold 5.0%)
//	[CheckMyBond RESOLVED] API error rate — back to normal
func (n *EmailNotifier) subject(note Notification) string {
	if note.Resolved {
		return fmt.Sprintf("[%s RESOLVED] %s — back to normal", n.appName(), note.Rule.Name)
	}
	return fmt.Sprintf("[%s %s] %s — %s (threshold %s)",
		n.appName(),
		strings.ToUpper(note.Rule.Severity),
		note.Rule.Name,
		formatValue(note.Rule, note.Observed),
		formatThreshold(note.Rule))
}

func (n *EmailNotifier) body(note Notification) string {
	var b strings.Builder

	if note.Resolved {
		fmt.Fprintf(&b, "%s has recovered.\n\n", note.Rule.Name)
		fmt.Fprintf(&b, "%s is now %s, back within the threshold of %s.\n",
			note.Rule.Metric, formatValue(note.Rule, note.Observed), formatThreshold(note.Rule))
		if !note.Event.StartedAt.IsZero() {
			fmt.Fprintf(&b, "It was firing for %s.\n", humanizeDuration(time.Since(note.Event.StartedAt)))
		}
		b.WriteString("\nNo action needed. This message closes the incident.\n")
		n.appendFooter(&b, note)
		return b.String()
	}

	fmt.Fprintf(&b, "%s %s its threshold.\n\n",
		note.Rule.Name, ComparatorPhrase(note.Rule.Comparator))

	fmt.Fprintf(&b, "  Measured    %s\n", formatValue(note.Rule, note.Observed))
	fmt.Fprintf(&b, "  Threshold   %s %s\n", ComparatorPhrase(note.Rule.Comparator), formatThreshold(note.Rule))
	fmt.Fprintf(&b, "  Severity    %s\n", note.Rule.Severity)
	fmt.Fprintf(&b, "  Metric      %s\n", note.Rule.Metric)
	fmt.Fprintf(&b, "  Measured over the last %s\n", humanizeDuration(note.Rule.Window()))

	if len(note.Rule.Labels) > 0 {
		b.WriteString("  Scope       ")
		parts := make([]string, 0, len(note.Rule.Labels))
		for k, v := range note.Rule.Labels {
			parts = append(parts, k+"="+v)
		}
		b.WriteString(strings.Join(parts, ", "))
		b.WriteByte('\n')
	}

	if note.Rule.Description != nil && *note.Rule.Description != "" {
		fmt.Fprintf(&b, "\nWhat this measures: %s\n", *note.Rule.Description)
	}

	// Concrete first step, chosen from the metric. Generic advice gets
	// ignored; a specific instruction gets followed.
	if hint := triageHint(note.Rule.Metric); hint != "" {
		fmt.Fprintf(&b, "\nWhere to look first: %s\n", hint)
	}

	n.appendFooter(&b, note)
	return b.String()
}

func (n *EmailNotifier) appendFooter(b *strings.Builder, note Notification) {
	b.WriteString("\n")
	if n.DashboardURL != "" {
		fmt.Fprintf(b, "Dashboard: %s\n", n.DashboardURL)
	}
	if note.TraceID != "" {
		// The trace id ties this email to the exact evaluation in the
		// structured logs — searching for it goes straight to the
		// surrounding context rather than a timestamp range.
		fmt.Fprintf(b, "Trace id: %s\n", note.TraceID)
	}
	fmt.Fprintf(b, "Fired at: %s\n", time.Now().UTC().Format("2006-01-02 15:04:05 UTC"))
	b.WriteString("\nYou're receiving this because your address is on the alert list for this rule.\n")
	b.WriteString("Manage recipients in the Alerts screen.\n")
}

// triageHint maps a metric to the first thing worth checking.
func triageHint(metric string) string {
	switch metric {
	case "db_up":
		return "whether Postgres is running and reachable from the app host, then the connection count against max_connections"
	case "redis_up":
		return "whether Redis is running — Celery queueing and the token blacklist both depend on it, so logins and uploads will be failing too"
	case "db_query_p95_ms", "db_query_p99_ms":
		return "the slow query log, and bondqi_db_pool_utilization_ratio — saturation here slows every endpoint at once"
	case "db_pool_utilization":
		return "long-running transactions holding connections; raising MaxConns treats the symptom, not the cause"
	case "http_error_rate_pct", "http_route_error_rate_pct":
		return "recent deploys first, then the audit_log rows with status_code >= 500 for the failing path"
	case "http_latency_p95_ms", "http_route_latency_p95_ms":
		return "database latency and pool utilization before suspecting the handlers themselves"
	case "queue_depth", "oldest_pending_job_minutes":
		return "whether the Celery workers are alive and consuming — a backlog with healthy workers means throughput, a backlog with dead workers means restart them"
	case "sla_breach_count":
		return "the review queue staffing and the extraction worker count"
	case "extraction_failure_rate_pct":
		return "the LLM provider's status, then the extraction worker logs for a repeated error kind"
	case "payment_failure_rate_pct", "payment_stuck_pending":
		return "the payment gateway's status page, then whether webhook deliveries are arriving"
	case "auth_failure_rate_per_min":
		return "whether the failures share a source IP (credential stuffing) or are spread across many (something broke in login)"
	case "coupon_redemptions_per_min", "coupon_discount_per_hour":
		return "the redemption list for the code involved — if one code dominates, it has leaked and should be switched off"
	case "confidence_drift":
		return "whether the model or prompt version changed, and which fields regressed in the drift report"
	case "goroutines", "heap_alloc_mb":
		return "whether the number has been climbing steadily (a leak) or spiked (load)"
	}
	return ""
}

func humanizeDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%d hours", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%.1f days", d.Hours()/24)
	}
}

// ── Test double ──────────────────────────────────────────────────────────────

// CapturingNotifier records notifications instead of sending them. Used by
// the engine tests, and usable as a dry-run notifier when tuning
// thresholds against production traffic without mailing anyone.
type CapturingNotifier struct {
	Sent []Notification
}

func (c *CapturingNotifier) Name() string { return "capture" }

func (c *CapturingNotifier) Notify(ctx context.Context, n Notification) error {
	c.Sent = append(c.Sent, n)
	return nil
}
