# backend/monitoring/instrument.py
#
# Wires monitoring/metrics.py into Celery's signal hooks and provides the
# `span()` helper for instrumenting arbitrary pipeline code -- the Python
# equivalent of internal/metrics.Span on the Go side.
#
# Kept separate from workers/celery_app.py's existing signal handlers
# (task_prerun/task_postrun already update ExtractionJob.started_at)
# rather than editing them in place, so the metrics wiring is one import
# away from being disabled/removed without touching the DB-update logic
# that was already there and already relied upon.
import logging
import time
from contextlib import contextmanager
from typing import Optional

from celery.signals import task_failure, task_postrun, task_prerun, task_retry

from monitoring.metrics import (
    stage_duration_seconds,
    stage_errors_total,
    task_duration_seconds,
    task_retries_total,
    tasks_in_flight,
    tasks_total,
)

logger = logging.getLogger("monitoring.instrument")

# task_id -> monotonic start time. A plain dict is fine: Celery workers
# are single-process-per-task under the prefork pool this project uses
# (see celery_app.conf — no eventlet/gevent), so there's no concurrent
# access to guard against within one process.
_task_starts: dict[str, float] = {}


@task_prerun.connect
def _on_task_start_metrics(task_id, task, args=None, kwargs=None, **extras):
    _task_starts[task_id] = time.monotonic()
    tasks_in_flight.labels(task_name=task.name).inc()


@task_postrun.connect
def _on_task_end_metrics(task_id, task, args=None, kwargs=None, retval=None, state=None, **extras):
    start = _task_starts.pop(task_id, None)
    tasks_in_flight.labels(task_name=task.name).dec()
    if start is not None:
        task_duration_seconds.labels(task_name=task.name).observe(time.monotonic() - start)
    # Celery's task_postrun state is the raw execution result ("SUCCESS",
    # "FAILURE", "RETRY", ...) rather than the extraction pipeline's own
    # status strings -- normalize to lowercase so it lines up with the
    # Go side's status labels (succeeded/failed/pending) at a glance in
    # Grafana, without literally renaming Celery's vocabulary.
    tasks_total.labels(task_name=task.name, state=(state or "unknown").lower()).inc()


@task_retry.connect
def _on_task_retry_metrics(sender=None, request=None, reason=None, **extras):
    name = sender.name if sender is not None else "unknown"
    task_retries_total.labels(task_name=name).inc()


@task_failure.connect
def _on_task_failure_metrics(sender=None, task_id=None, exception=None, **extras):
    # task_postrun's "FAILURE" state already counts this task-level
    # failure in tasks_total; this signal exists so the failure also
    # shows up with its exception's class name attached, which
    # task_postrun's generic state string does not carry.
    name = sender.name if sender is not None else "unknown"
    kind = type(exception).__name__ if exception is not None else "unknown"
    stage_errors_total.labels(stage="task", step=name, error_kind=kind).inc()


@contextmanager
def span(stage: str, step: str, *, field_name: Optional[str] = None):
    """Times one named piece of pipeline work and records it as a stage
    metric, in addition to the existing plain-log timing in
    utils/timing.py.

    Usage mirrors utils.timing.timed() on purpose -- the two often wrap
    the same block during development:

        from monitoring.instrument import span
        with span("extraction", "llm_call", field_name="Total Revenue"):
            ...

    A raised exception inside the block is recorded as a stage error
    (bounded to the exception's class name, so this never becomes an
    unbounded label) and re-raised unchanged -- this is an observer, not
    a handler.
    """
    start = time.monotonic()
    try:
        yield
    except Exception as exc:
        stage_errors_total.labels(stage=stage, step=step, error_kind=type(exc).__name__).inc()
        raise
    finally:
        stage_duration_seconds.labels(stage=stage, step=step).observe(time.monotonic() - start)
