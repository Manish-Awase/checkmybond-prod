# backend/monitoring/metrics.py
#
# Prometheus metrics for the Celery worker side of the pipeline.
#
# Deliberately in the same bondqi_* namespace and label shape as
# hitl-go/internal/metrics, so a Grafana panel can sum a metric like
# bondqi_span_duration_seconds across both languages without a
# language-specific query. Where the Go side is a hand-rolled registry
# (no network access to fetch client_golang in that environment), this
# side uses the real prometheus_client package since it's a normal pip
# dependency here.
#
# Exposed on its own HTTP port (METRICS_PORT, default 9200) via
# prometheus_client's built-in server -- the worker process has no other
# HTTP framework (FastAPI/uvicorn are intentionally absent, see
# requirements.txt), and pulling one in just for a /metrics endpoint
# would be a heavier dependency than the one-line built-in server.
import logging
import os

from prometheus_client import (
    CollectorRegistry,
    Counter,
    Gauge,
    Histogram,
    start_http_server,
)

logger = logging.getLogger("monitoring")

registry = CollectorRegistry()

# Buckets mirror internal/metrics.LLMBuckets on the Go side -- extraction
# work here is LLM-call-bound, so the default web-latency buckets
# (topping out around 10s) would bucket almost everything into the last
# one and tell you nothing.
LLM_BUCKETS = (0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30, 60, 120, 300)
DEFAULT_BUCKETS = (0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30)

# ── Task-level metrics ───────────────────────────────────────────────────────

tasks_total = Counter(
    "bondqi_worker_tasks_total",
    "Celery tasks processed, by task name and final state.",
    ["task_name", "state"],
    registry=registry,
)

task_duration_seconds = Histogram(
    "bondqi_worker_task_duration_seconds",
    "Celery task wall-clock duration.",
    ["task_name"],
    buckets=LLM_BUCKETS,
    registry=registry,
)

tasks_in_flight = Gauge(
    "bondqi_worker_tasks_in_flight",
    "Celery tasks currently executing on this worker.",
    ["task_name"],
    registry=registry,
)

task_retries_total = Counter(
    "bondqi_worker_task_retries_total",
    "Celery task retries, by task name.",
    ["task_name"],
    registry=registry,
)

# ── Extraction pipeline stage metrics ────────────────────────────────────────
#
# One histogram, stage-labelled, rather than a histogram per stage --
# keeps `sum by (stage) (rate(bondqi_stage_duration_seconds_sum[5m]))`
# a single query instead of N.

stage_duration_seconds = Histogram(
    "bondqi_stage_duration_seconds",
    "Duration of one named stage of the extraction pipeline "
    "(retrieval, prompt_build, llm_call, parse, validate, ...).",
    ["stage", "step"],
    buckets=LLM_BUCKETS,
    registry=registry,
)

stage_errors_total = Counter(
    "bondqi_stage_errors_total",
    "Stage failures, by stage/step and a bounded error kind.",
    ["stage", "step", "error_kind"],
    registry=registry,
)

extraction_confidence = Histogram(
    "bondqi_extraction_confidence",
    "Confidence score attached to an extracted field (0-1).",
    ["field_name"],
    buckets=(0.0, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.85, 0.9, 0.95, 1.0),
    registry=registry,
)

# ── LLM call metrics ─────────────────────────────────────────────────────────

llm_calls_total = Counter(
    "bondqi_llm_calls_total",
    "LLM API calls, by model and outcome (success/error/timeout).",
    ["model", "outcome"],
    registry=registry,
)

llm_call_duration_seconds = Histogram(
    "bondqi_llm_call_duration_seconds",
    "LLM API call latency.",
    ["model"],
    buckets=LLM_BUCKETS,
    registry=registry,
)

llm_tokens_total = Counter(
    "bondqi_llm_tokens_total",
    "LLM tokens consumed, by model and kind (prompt/completion).",
    ["model", "kind"],
    registry=registry,
)

llm_cost_usd = Counter(
    "bondqi_llm_cost_usd_total",
    "Real dollar cost of LLM calls, by model -- computed from each "
    "provider's own reported token usage against services/llm_client.py's "
    "PRICING_USD_PER_1M table, not estimated. Compare against revenue "
    "per field (hitl-go's plan pricing) to see actual margin, not an "
    "assumed one -- see record_llm_cost in llm_client.py.",
    ["model"],
    registry=registry,
)

# ── Queue / SLA metrics ───────────────────────────────────────────────────────

review_queue_depth = Gauge(
    "bondqi_worker_review_queue_depth",
    "Items currently waiting in the human-review SLA queue.",
    registry=registry,
)

sla_breaches_total = Counter(
    "bondqi_worker_sla_breaches_total",
    "Extraction jobs that missed their SLA deadline.",
    registry=registry,
)

# ── Worker health ────────────────────────────────────────────────────────────

worker_up = Gauge(
    "bondqi_worker_up",
    "1 while this worker process is alive; set at start and cleared "
    "in the shutdown signal so a killed worker's last-seen value is 0, "
    "not a stale 1 that a scrape gap could hide.",
    ["worker_name"],
    registry=registry,
)

db_errors_total = Counter(
    "bondqi_worker_db_errors_total",
    "Database errors encountered by worker code, by operation.",
    ["operation"],
    registry=registry,
)

_server_started = False


def start_metrics_server() -> None:
    """Starts the Prometheus HTTP endpoint. Call once, from worker_init.

    Safe to call more than once (e.g. under a supervisor that re-runs
    worker_init) -- a second call is a no-op rather than an
    "address already in use" crash.
    """
    global _server_started
    if _server_started:
        return
    port = int(os.getenv("METRICS_PORT", "9200"))
    try:
        start_http_server(port, registry=registry)
        _server_started = True
        logger.info("Prometheus metrics server listening on :%d/metrics", port)
    except OSError as e:
        # Most likely two worker processes on one host sharing a port.
        # Don't crash the worker over a metrics port collision -- the
        # pipeline still needs to run even if this one instance's
        # metrics aren't scrapeable.
        logger.error("Could not start metrics server on :%d — %s", port, e)
