# backend/utils/timing.py
#
# Lightweight timing context manager used throughout the extraction
# pipeline (services/extractor.py, services/evaluator.py) to log how long
# each stage takes — retrieval, prompt building, the LLM call itself,
# parsing/validation.
#
# Also records a bondqi_stage_duration_seconds histogram sample
# (monitoring/metrics.py) alongside the existing log line, so the same
# call site backs both the free-text debug log AND a queryable Grafana
# panel. The metrics import is optional at import time -- monitoring/
# depends on prometheus_client, and a worker environment that hasn't
# picked that dependency up yet should still get the logging behavior
# this function always had, not an ImportError on a module many other
# files already call.
#
# Usage:
#   with timed("extraction", "llm_call", field_name="Total Revenue", meta={"model": "..."}):
#       ...
import logging
import time
from contextlib import contextmanager

logger = logging.getLogger("timing")

try:
    from monitoring.metrics import stage_duration_seconds
except ImportError:  # pragma: no cover - degrades to log-only, see above
    stage_duration_seconds = None


@contextmanager
def timed(stage: str, step: str, field_name: str | None = None, meta: dict | None = None):
    start = time.monotonic()
    try:
        yield
    finally:
        elapsed_ms = (time.monotonic() - start) * 1000
        extra = f" {meta}" if meta else ""
        field = f" field={field_name}" if field_name else ""
        logger.info("[timing] %s.%s took %.1fms%s%s", stage, step, elapsed_ms, field, extra)
        if stage_duration_seconds is not None:
            stage_duration_seconds.labels(stage=stage, step=step).observe(elapsed_ms / 1000)
