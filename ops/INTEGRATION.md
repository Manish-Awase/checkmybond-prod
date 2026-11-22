# ops/INTEGRATION.md

Environment variables and startup steps for the metrics/alerting/coupon
subsystems. This is the doc referenced from the comment in
`hitl-go/internal/metrics/db.go`.

## 1. Run the migrations

```bash
psql "$DATABASE_URL" -f migrations/0003_coupons.sql
psql "$DATABASE_URL" -f migrations/0004_alerting.sql
```

`0004_alerting.sql` seeds 12 starting alert rules (API error rate, DB/Redis
down, queue depth, SLA breaches, extraction/payment failure rates, login
failures, coupon abuse, confidence drift, ...). No email recipients are
seeded — add at least one from the Alerts admin page (`/admin/alerts`)
or nothing will actually receive mail when a rule fires.

## 2. Environment variables

### Metrics (`internal/metrics`)

| Variable | Default | Meaning |
|---|---|---|
| `METRICS_PATH` | `/metrics` | Where the Prometheus scrape handler is mounted |
| `METRICS_TOKEN` | unset | If set, `/metrics` requires `Authorization: Bearer <token>` |
| `METRICS_ALLOW_CIDR` | unset | If set, `/metrics` only answers requests from this CIDR |
| `SLOW_QUERY_MS` | `500` | DB queries slower than this get a `slow_query` warn log |
| `LOG_FORMAT` | `text` | `json` for structured logs (recommended once you're shipping them anywhere) |
| `SERVICE_NAME` | derived per-binary | Overrides the `service` build-info label |
| `APP_VERSION` | unset | Populates the `version` build-info label |

Set at least one of `METRICS_TOKEN` / `METRICS_ALLOW_CIDR` before
exposing `/metrics` on anything but a private network — it reports route
names, error rates, and revenue counters.

### Alerting (`internal/alerting`)

| Variable | Default | Meaning |
|---|---|---|
| `ALERT_EVAL_INTERVAL_SECONDS` | `30` | How often the engine re-checks every active rule |
| `GRAFANA_DASHBOARD_URL` | unset | Included as a link in alert emails, if set |
| `EMAIL_PROVIDER` | `sendgrid` | Shared with the rest of the app — `sendgrid` / `brevo` / `smtp` / `stdout` |
| `SENDGRID_API_KEY`, `SENDGRID_FROM_EMAIL` | — | Required when `EMAIL_PROVIDER=sendgrid` |
| `BREVO_API_KEY`, `BREVO_FROM_EMAIL` | — | Required when `EMAIL_PROVIDER=brevo` |
| `APP_NAME` | `BondQi` | Used in the alert email subject line |

Switching mail providers — e.g. from SendGrid to Brevo — is a config-only
change: set `EMAIL_PROVIDER=brevo` plus `BREVO_API_KEY` and
`BREVO_FROM_EMAIL`, and redeploy. No code changes needed; every call
site (password reset, login OTP, contact form, alert emails) goes
through one internal switch in `internal/email/email.go` rather than
calling a specific provider directly, so this is the only thing that
needs touching to move providers.

With `EMAIL_PROVIDER=stdout`, or `sendgrid`/`brevo` selected but their
credentials unset, alert
emails print to the server log instead of sending — useful in
development, but the Alerts admin page's status panel will flag this
("no active recipients" / provider shown) so it isn't discovered for the
first time during a real incident.

### Worker metrics (`backend/monitoring`)

| Variable | Default | Meaning |
|---|---|---|
| `METRICS_PORT` | `9200` | Port the Celery worker's Prometheus endpoint listens on |

Running more than one worker replica on one host needs a distinct
`METRICS_PORT` per replica, and a corresponding line in Prometheus's
`worker` scrape job.

## 3. What's wired where

- `cmd/server/main.go` — mounts `metrics.Handler()`, starts DB/Redis
  health probes, starts the alert engine, mounts `/api/coupons` and
  `/api/alerts`.
- `gateway/main.go` — separate `/metrics` for edge traffic (includes
  requests the backend never sees: rejected JWTs, rate-limited callers).
- `backend/workers/celery_app.py` — starts the worker's metrics HTTP
  server and Celery signal hooks from `worker_init`.
- `routers/payment.go` — coupon codes applied at `/api/payment/calculate`
  and consumed atomically inside `/api/payment/pay`, with a compensating
  release on charge failure or abandoned verification.

## 4. Verifying it's working

```bash
curl localhost:8000/metrics | head -30      # hitl-server
curl localhost:9000/metrics | head -30      # gateway
curl localhost:9200/metrics | head -30      # a worker
curl localhost:8000/ready                   # readiness (checks DB)
```

From the Alerts admin page, "Send test alert" proves the email path
end-to-end without waiting for a real threshold breach.
