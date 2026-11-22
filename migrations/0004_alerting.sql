-- migrations/0004_alerting.sql
--
-- Alert service: rules, email recipients, and fired-alert history.
--
--   psql "$DATABASE_URL" -f migrations/0004_alerting.sql
--
-- Safe to re-run.
--
-- HOW THE THREE TABLES RELATE
--
--   alert_rules       — what counts as a problem ("5xx rate above 5%")
--   alert_recipients  — who hears about it (the email addresses you asked
--                       to keep in the database; one row per address, and
--                       a row can be scoped to one rule or left global)
--   alert_events      — what actually happened, one row per firing, kept
--                       as history and used for the repeat-notification
--                       cooldown so a flapping metric can't send 400 mails
--
-- `metric` is a key the evaluator in internal/alerting/engine.go knows how
-- to compute — not free-form SQL. Adding a new alertable quantity means
-- adding a resolver there, which keeps arbitrary user-supplied SQL out of
-- a table that an admin UI writes to. The full list of valid keys is in
-- internal/alerting/metrics_catalog.go and is also served to the UI at
-- GET /api/alerts/catalog, so the dropdown can never drift from what the
-- engine actually supports.

CREATE TABLE IF NOT EXISTS alert_rules (
    id              VARCHAR       PRIMARY KEY,
    name            VARCHAR(128)  NOT NULL UNIQUE,
    description     TEXT          NULL,

    metric          VARCHAR(64)   NOT NULL,   -- see internal/alerting/metrics_catalog.go
    comparator      VARCHAR(8)    NOT NULL DEFAULT 'gt',  -- gt | gte | lt | lte | eq
    threshold       DOUBLE PRECISION NOT NULL,
    severity        VARCHAR(16)   NOT NULL DEFAULT 'warning', -- info | warning | critical

    -- Lookback the metric is computed over (e.g. "5xx rate over the last
    -- 5 minutes"). Ignored by point-in-time metrics like queue_depth.
    window_seconds  INTEGER       NOT NULL DEFAULT 300,

    -- The condition must hold continuously for this long before the alert
    -- fires. This is what stops a single slow request from paging anyone.
    -- 0 fires on the first breaching evaluation.
    for_seconds     INTEGER       NOT NULL DEFAULT 60,

    -- Minimum gap between repeat emails while an alert stays firing.
    cooldown_seconds INTEGER      NOT NULL DEFAULT 1800,

    -- Optional narrowing, e.g. {"route":"/api/documents/upload"} for
    -- route-scoped latency rules, or {"service":"celery"}. Interpreted by
    -- the individual resolver; unknown keys are ignored.
    labels          TEXT          NULL,       -- JSON object as text

    is_active       BOOLEAN       NOT NULL DEFAULT true,
    created_by      VARCHAR       NULL,
    created_at      TIMESTAMP     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMP     NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_alert_rules_active ON alert_rules(is_active);

-- THE ALERT EMAIL TABLE.
--
-- rule_id NULL means "send me everything at or above severity_min" — the
-- on-call inbox. A non-NULL rule_id scopes an address to one specific
-- problem, which is how you send database alerts to the DBA and payment
-- alerts to whoever owns billing without them reading each other's mail.
CREATE TABLE IF NOT EXISTS alert_recipients (
    id           VARCHAR      PRIMARY KEY,
    email        VARCHAR(255) NOT NULL,
    label        VARCHAR(128) NULL,          -- "on-call", "billing", a person's name
    rule_id      VARCHAR      NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    severity_min VARCHAR(16)  NOT NULL DEFAULT 'warning', -- info | warning | critical
    is_active    BOOLEAN      NOT NULL DEFAULT true,
    created_at   TIMESTAMP    NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_alert_recipients_rule   ON alert_recipients(rule_id);
CREATE INDEX IF NOT EXISTS idx_alert_recipients_active ON alert_recipients(is_active);

-- One row per firing. state flips to 'resolved' (and resolved_at is set)
-- when the metric comes back under threshold, so this doubles as an
-- incident log you can query for "how often does this break, and for how
-- long".
CREATE TABLE IF NOT EXISTS alert_events (
    id              VARCHAR      PRIMARY KEY,
    rule_id         VARCHAR      NULL REFERENCES alert_rules(id) ON DELETE SET NULL,
    rule_name       VARCHAR(128) NOT NULL,   -- denormalized: survives rule deletion
    metric          VARCHAR(64)  NOT NULL,
    severity        VARCHAR(16)  NOT NULL,
    comparator      VARCHAR(8)   NOT NULL,
    threshold       DOUBLE PRECISION NOT NULL,
    observed_value  DOUBLE PRECISION NOT NULL,
    state           VARCHAR(16)  NOT NULL DEFAULT 'firing', -- firing | resolved
    message         TEXT         NULL,
    trace_id        VARCHAR(64)  NULL,       -- correlates to the structured logs
    notify_count    INTEGER      NOT NULL DEFAULT 0,
    notify_error    TEXT         NULL,
    started_at      TIMESTAMP    NOT NULL DEFAULT now(),
    last_notified_at TIMESTAMP   NULL,
    resolved_at     TIMESTAMP    NULL
);

CREATE INDEX IF NOT EXISTS idx_alert_events_rule    ON alert_events(rule_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_state   ON alert_events(state);
CREATE INDEX IF NOT EXISTS idx_alert_events_started ON alert_events(started_at DESC);

-- ── Seed: a starting rule set ───────────────────────────────────────────────
-- These are deliberately conservative starting thresholds, not tuned
-- numbers — tune them against a week of real traffic before trusting the
-- pager. Every one is editable from the Alerts admin UI.
INSERT INTO alert_rules (id, name, description, metric, comparator, threshold, severity, window_seconds, for_seconds, cooldown_seconds, created_by)
VALUES
  ('rule_api_5xx',      'API 5xx error rate',      'Share of requests returning 5xx across all routes.',                 'http_error_rate_pct',        'gt',  5,     'critical', 300,  120, 1800, 'seed'),
  ('rule_api_latency',  'API p95 latency',         'p95 request latency across all routes, in milliseconds.',            'http_latency_p95_ms',        'gt',  2000,  'warning',  300,  180, 1800, 'seed'),
  ('rule_db_down',      'Database unreachable',    'Postgres ping fails. 1 = reachable, 0 = down.',                      'db_up',                      'lt',  1,     'critical', 60,   0,   600,  'seed'),
  ('rule_db_latency',   'Database query latency',  'p95 time spent inside Postgres queries, in milliseconds.',           'db_query_p95_ms',            'gt',  500,   'warning',  300,  180, 1800, 'seed'),
  ('rule_redis_down',   'Redis unreachable',       'Redis ping fails. 1 = reachable, 0 = down.',                         'redis_up',                   'lt',  1,     'critical', 60,   0,   600,  'seed'),
  ('rule_queue_depth',  'Extraction queue backlog','Jobs sitting in pending or processing.',                             'queue_depth',                'gt',  100,   'warning',  60,   300, 3600, 'seed'),
  ('rule_sla_breach',   'SLA breaches',            'Jobs past their SLA deadline and still not complete.',               'sla_breach_count',           'gt',  5,     'critical', 60,   60,  1800, 'seed'),
  ('rule_extract_fail', 'Extraction failure rate', 'Share of extraction jobs ending in failed status.',                  'extraction_failure_rate_pct','gt',  20,    'critical', 900,  300, 1800, 'seed'),
  ('rule_payment_fail', 'Payment failure rate',    'Share of plan_transactions rows with status failed.',                'payment_failure_rate_pct',   'gt',  25,    'critical', 900,  120, 1800, 'seed'),
  ('rule_login_fail',   'Login failure spike',     'Failed auth responses per minute — brute force or a broken login.',  'auth_failure_rate_per_min',  'gt',  30,    'warning',  300,  120, 1800, 'seed'),
  ('rule_coupon_abuse', 'Coupon redemption spike', 'Coupon redemptions per minute — abuse or a leaked code.',            'coupon_redemptions_per_min', 'gt',  20,    'warning',  300,  120, 3600, 'seed'),
  ('rule_confidence',   'Extraction confidence drift','Drop in mean confidence vs the prior baseline week, in points.', 'confidence_drift',           'gt',  0.1,   'warning',  86400,0,   86400,'seed')
ON CONFLICT (id) DO NOTHING;
