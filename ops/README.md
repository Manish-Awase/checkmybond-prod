# ops/README.md

Prometheus + Grafana for BondQi. This directory only contains the
*observability* stack — Postgres/Redis/the app services live in their
usual place in the repo.

## Quick start

```bash
docker compose -f ops/docker-compose.observability.yml up -d
```

- Prometheus: http://localhost:9090
- Grafana: http://localhost:3001 (`admin` / `admin` — you'll be asked to
  change it on first login)

Grafana comes pre-provisioned with a Prometheus datasource and the
**BondQi Overview** dashboard (`grafana/dashboards/bondqi-overview.json`)
— no manual setup needed, it should just be there under the BondQi
folder.

## What's being scraped

`prometheus/prometheus.yml` scrapes three targets by default, assuming
the app processes run on the host and Prometheus runs in Docker:

| Service     | Port | Path      |
|-------------|------|-----------|
| hitl-server | 8000 | `/metrics`|
| gateway     | 9000 | `/metrics`|
| worker      | 9200 | `/metrics`|

If you run the app services in containers on the same compose network
instead, switch the targets to service names — the commented block at
the bottom of `prometheus.yml` shows the swap.

Multiple Celery worker replicas each need their own `METRICS_PORT` (see
`INTEGRATION.md`) and their own line in the `worker` job's
`static_configs`.

## Alerting: two layers, on purpose

There are two alerting paths and they're not redundant with each other:

1. **`internal/alerting` (the Go alert engine)** — reads its own
   in-process rolling windows, evaluates the rules in the
   `alert_rules` table, and emails `alert_recipients` directly. Keeps
   working even if Prometheus is down, because it doesn't depend on
   Prometheus at all. This is what the Alerts admin page manages.
2. **`prometheus/rules/bondqi-alerts.yml`** — standard Prometheus
   alerting rules over the same `bondqi_*` metrics, for anyone who
   wants Alertmanager routing to Slack/PagerDuty/etc., or wants
   Grafana's alert list panel to reflect the same conditions.

Thresholds are duplicated by hand between the two — there's no single
source of truth. If you tune a threshold in the Alerts admin page,
consider updating the matching Prometheus rule too, and vice versa.

## Files

```
ops/
├── docker-compose.observability.yml   Prometheus + Grafana only
├── prometheus/
│   ├── prometheus.yml                 scrape targets
│   └── rules/bondqi-alerts.yml        Prometheus alerting rules
├── grafana/
│   ├── dashboards/bondqi-overview.json
│   └── provisioning/{datasources,dashboards}/
├── README.md                          this file
└── INTEGRATION.md                     env vars, migrations, wiring details
```

See `INTEGRATION.md` in this same directory for the environment
variables and migration steps needed on the app side before there's
anything here to scrape.
