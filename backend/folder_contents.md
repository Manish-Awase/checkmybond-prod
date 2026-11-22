# backend

## Files Present

`backend\.dockerignore`
`backend\Dockerfile`
`backend\hitl\.gitkeep`
`backend\hitl\__init__.py`
`backend\hitl\correction_db.py`
`backend\hitl\failure_tagger.py`
`backend\hitl\sla_queue.py`
`backend\models\.gitkeep`
`backend\models\__init__.py`
`backend\models\db.py`
`backend\models\db_backup_20260723_203132.json`
`backend\monitoring\.gitkeep`
`backend\monitoring\__init__.py`
`backend\monitoring\drift_detector.py`
`backend\requirements.txt`
`backend\services\__init__.py`
`backend\services\confidence_gate.py`
`backend\services\evaluator.py`
`backend\services\extractor.py`
`backend\services\normalizer.py`
`backend\services\ocr_extractor.py`
`backend\services\parser_service.py`
`backend\services\prompt_registry.py`
`backend\services\reextractor.py`
`backend\services\schema_service.py`
`backend\services\table_tagger.py`
`backend\services\vector_store.py`
`backend\utils\__init__.py`
`backend\utils\db_backup.py`
`backend\utils\db_pool.py`
`backend\utils\timing.py`
`backend\workers\__init__.py`
`backend\workers\celery_app.py`
`backend\workers\pending_cache.py`
`backend\workers\recovery.py`

## File Contents

### `backend\.dockerignore`

```
__pycache__/
*.pyc
.venv/
venv/
*.egg-info/
.pytest_cache/

```

---

### `backend\Dockerfile`

```
# backend/Dockerfile
# Python Celery worker — runs the ML extraction pipeline.
# No HTTP server inside this image — workers only (HTTP is the Go server).

FROM python:3.12-slim

WORKDIR /app

# System deps for pytesseract (OCR binary) and Pillow/image libs
RUN apt-get update && apt-get install -y --no-install-recommends \
    tesseract-ocr \
    libgl1 \
    && rm -rf /var/lib/apt/lists/*

RUN pip install "setuptools<82"

# Heavy ML libs that RARELY change — cached as their own layer.
# CPU-only torch build keeps the image far smaller than the CUDA default.
RUN pip install torch \
    --index-url https://download.pytorch.org/whl/cpu \
    --timeout 300 --retries 10

RUN pip install sentence-transformers \
    --timeout 300 --retries 10

# For Word → PDF
RUN apt-get update && apt-get install -y libreoffice-writer --no-install-recommends \
    && rm -rf /var/lib/apt/lists/*   
# worth adding — cleans apt cache, keeps image smaller

# Frequently changing requirements — separate layer so edits here don't
# invalidate the torch/sentence-transformers cache above.
COPY requirements.txt .
RUN pip install --no-cache-dir --timeout 300 --retries 10 -r requirements.txt

COPY . .

# Mount points — populated via docker-compose volumes
RUN mkdir -p /app/export /app/uploads /app/database

ENV PYTHONUNBUFFERED=1

# Health: container is healthy if celery responds to inspect ping
HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
    CMD celery -A workers.celery_app inspect ping -d celery@$HOSTNAME || exit 1

# This image runs the Celery worker, not an HTTP server — main.py does
# not exist in this project, all HTTP is handled by hitl-go/.
CMD ["celery", "-A", "workers.celery_app", "worker", \
     "--loglevel=info", "--concurrency=2"]

```

---

### `backend\hitl\.gitkeep`

```

```

---

### `backend\hitl\__init__.py`

```python
# backend/hitl/__init__.py

```

---

### `backend\hitl\correction_db.py`

```python
# backend/hitl/correction_db.py
"""
Correction persistence layer.
Wraps DB writes for correction records and DPO pair export.
"""
import json
import logging
import os
import uuid
from datetime import datetime

from models.db import SessionLocal, Correction

logger     = logging.getLogger("correction_db")
EXPORT_DIR = os.getenv("EXPORT_DIR", "export")


def save_correction(
    extraction_id:   str,
    field_name:      str,
    original_value:  str,
    corrected_value: str,
    failure_type:    str,
    analyst_id:      str,
    doc_hash:        str | None = None,
    schema_id:       str | None = None,
    prompt_id:       str | None = None,
    retrieved_chunk_ids: list[str] | None = None,
    confidence_score:    float | None = None,
    context_relevance:   float | None = None,
    answer_relevance:    float | None = None,
    groundedness:        float | None = None,
) -> Correction:
    """Persist a correction record and export a DPO training pair."""
    db = SessionLocal()
    try:
        correction = Correction(
            id                  = str(uuid.uuid4()),
            extraction_id       = extraction_id,
            doc_hash            = doc_hash,
            field_name          = field_name,
            original_value      = original_value,
            corrected_value     = corrected_value,
            failure_type        = failure_type,
            analyst_id          = analyst_id,
            schema_id           = schema_id,
            prompt_id           = prompt_id,
            retrieved_chunk_ids = json.dumps(retrieved_chunk_ids) if retrieved_chunk_ids else None,
            confidence_score    = confidence_score,
            context_relevance   = context_relevance,
            answer_relevance    = answer_relevance,
            groundedness        = groundedness,
        )
        db.add(correction)
        db.commit()
        db.refresh(correction)

        _export_dpo_pair(correction)
        logger.info("Saved correction id=%s field=%s", correction.id, field_name)
        return correction

    except Exception as e:
        db.rollback()
        logger.error("Failed to save correction: %s", e)
        raise
    finally:
        db.close()


def _export_dpo_pair(correction: Correction) -> None:
    """Append a DPO training pair to export/dpo_pairs.jsonl."""
    os.makedirs(EXPORT_DIR, exist_ok=True)
    pair = {
        "correction_id":   correction.id,
        "extraction_id":   correction.extraction_id,
        "field_name":      correction.field_name,
        "original_value":  correction.original_value,
        "corrected_value": correction.corrected_value,
        "failure_type":    correction.failure_type,
        "prompt_id":       correction.prompt_id,
        "confidence":      correction.confidence_score,
        "exported_at":     datetime.utcnow().isoformat(),
    }
    fpath = os.path.join(EXPORT_DIR, "dpo_pairs.jsonl")
    try:
        with open(fpath, "a") as f:
            f.write(json.dumps(pair) + "\n")
    except OSError as e:
        logger.error("DPO export failed: %s", e)

```

---

### `backend\hitl\failure_tagger.py`

```python
# backend/hitl/failure_tagger.py
"""
Failure taxonomy — maps extraction outcomes to structured failure types.
Used by the confidence gate and HITL review UI.
"""
from enum import Enum


class FailureType(str, Enum):
    PARSER_FAILURE     = "parser_failure"      # PDF couldn't be parsed
    RETRIEVAL_FAILURE  = "retrieval_failure"   # Wrong chunks returned
    REASONING_FAILURE  = "reasoning_failure"   # Correct chunks, wrong value
    SCHEMA_FAILURE     = "schema_failure"      # Field name didn't match document
    VALIDATION_FAILURE = "validation_failure"  # Value wrong format/type
    UNKNOWN            = "unknown"


FAILURE_DESCRIPTIONS: dict[str, str] = {
    FailureType.PARSER_FAILURE:     "PDF couldn't be parsed — empty text, OCR issue",
    FailureType.RETRIEVAL_FAILURE:  "Wrong chunks returned — embedding mismatch",
    FailureType.REASONING_FAILURE:  "Correct chunks, but wrong value extracted",
    FailureType.SCHEMA_FAILURE:     "Field name didn't match document terminology",
    FailureType.VALIDATION_FAILURE: "Value extracted but wrong format/type",
    FailureType.UNKNOWN:            "Failure type not yet classified",
}

FAILURE_FIXES: dict[str, str] = {
    FailureType.PARSER_FAILURE:     "Add OCR fallback, check bounding boxes",
    FailureType.RETRIEVAL_FAILURE:  "Domain-tune embeddings, add reranker layer",
    FailureType.REASONING_FAILURE:  "Improve prompt engineering, use structured output",
    FailureType.SCHEMA_FAILURE:     "Add more aliases to field definition",
    FailureType.VALIDATION_FAILURE: "Add field-type validators and confidence scoring",
    FailureType.UNKNOWN:            "Review system logs",
}


def infer_failure_type(
    extraction_result: dict,
    eval_scores: dict,
) -> FailureType:
    """
    Automatically infer failure type from extraction + evaluation signals.
    Used by confidence_gate.py when routing to human review.
    """
    raw_value        = extraction_result.get("raw_value", "")
    is_valid         = extraction_result.get("is_valid", False)
    guard_reason     = extraction_result.get("guard_reason", "")
    context_rel      = eval_scores.get("context_relevance", 1.0)
    groundedness     = eval_scores.get("groundedness", 1.0)
    confidence       = eval_scores.get("confidence_score", 1.0)

    if "no_chunks_retrieved" in guard_reason or "llm_error" in guard_reason:
        return FailureType.PARSER_FAILURE

    if context_rel < 0.3:
        return FailureType.RETRIEVAL_FAILURE

    if not is_valid and raw_value not in ("", "EXTRACTION_FAILED"):
        return FailureType.VALIDATION_FAILURE

    if groundedness < 0.3:
        return FailureType.REASONING_FAILURE

    if "alias" in guard_reason.lower() or "schema" in guard_reason.lower():
        return FailureType.SCHEMA_FAILURE

    return FailureType.UNKNOWN


def get_description(failure_type: str) -> str:
    return FAILURE_DESCRIPTIONS.get(failure_type, FAILURE_DESCRIPTIONS[FailureType.UNKNOWN])


def get_fix(failure_type: str) -> str:
    return FAILURE_FIXES.get(failure_type, FAILURE_FIXES[FailureType.UNKNOWN])

```

---

### `backend\hitl\sla_queue.py`

```python
# backend/hitl/sla_queue.py
"""
SLA-aware review queue backed by Redis sorted set.
Score = SLA deadline unix timestamp → lowest score = most urgent item.

Used by hitl.py routes. Go's hitl.go replaces the HTTP layer but
this class is still used internally by the Python worker pipeline
to push items for human review.
"""
import json
import logging
import os
import time
from datetime import datetime, timedelta

import redis

logger    = logging.getLogger("sla_queue")
REDIS_HOST = os.getenv("REDIS_HOST", "localhost")
REDIS_URL  = os.getenv("REDIS_URL", f"redis://{REDIS_HOST}:6379/0")

QUEUE_KEY       = "hitl:review_queue"
ESCALATION_KEY  = "hitl:escalations"
SLA_HOURS       = int(os.getenv("SLA_HOURS", "24"))


class SLAQueue:
    def __init__(self):
        self._r = redis.from_url(REDIS_URL, decode_responses=True)

    # ── Write ─────────────────────────────────────────────────────────────────

    def push(self, item: dict, sla_hours: int = SLA_HOURS) -> None:
        """
        Push a review item. Score = SLA deadline unix timestamp.
        Lower score = higher priority (expires sooner).
        """
        deadline = time.time() + (sla_hours * 3600)
        item["sla_deadline"] = datetime.utcfromtimestamp(deadline).isoformat()
        item["queued_at"]    = datetime.utcnow().isoformat()
        try:
            self._r.zadd(QUEUE_KEY, {json.dumps(item): deadline})
            logger.info("Queued item_id=%s deadline=%s", item.get("item_id"), item["sla_deadline"])
        except redis.RedisError as e:
            logger.error("Queue push failed: %s", e)

    # ── Read ──────────────────────────────────────────────────────────────────

    def pop_next(self) -> dict | None:
        """
        Atomically pop the highest-priority (lowest score = soonest SLA) item.
        Returns None if queue is empty.
        """
        try:
            results = self._r.zpopmin(QUEUE_KEY, count=1)
            if not results:
                return None
            raw, score = results[0]
            item = json.loads(raw)
            # Flag as escalation if SLA already breached
            if score < time.time():
                self._escalate(item)
            return item
        except redis.RedisError as e:
            logger.error("Queue pop failed: %s", e)
            return None

    def peek(self, limit: int = 50) -> list[dict]:
        """Return up to `limit` items without removing them, ordered by urgency."""
        try:
            results = self._r.zrange(QUEUE_KEY, 0, limit - 1, withscores=True)
            items = []
            now = time.time()
            for raw, score in results:
                item = json.loads(raw)
                item["overdue"] = score < now
                items.append(item)
            return items
        except redis.RedisError as e:
            logger.error("Queue peek failed: %s", e)
            return []

    def depth(self) -> int:
        """Total items in the queue."""
        try:
            return self._r.zcard(QUEUE_KEY)
        except redis.RedisError:
            return 0

    # ── Escalations ───────────────────────────────────────────────────────────

    def _escalate(self, item: dict) -> None:
        """Move an overdue item to the escalation set."""
        try:
            item["escalated_at"] = datetime.utcnow().isoformat()
            self._r.sadd(ESCALATION_KEY, json.dumps(item))
            logger.warning("Escalated overdue item: %s", item.get("item_id"))
        except redis.RedisError as e:
            logger.error("Escalation failed: %s", e)

    def get_escalations(self) -> list[dict]:
        """Return all escalated items."""
        try:
            return [json.loads(raw) for raw in self._r.smembers(ESCALATION_KEY)]
        except redis.RedisError as e:
            logger.error("Get escalations failed: %s", e)
            return []

    def clear_escalations(self) -> dict:
        """Clear all escalation entries (admin action)."""
        try:
            count = self._r.scard(ESCALATION_KEY)
            self._r.delete(ESCALATION_KEY)
            logger.info("Cleared %d escalations", count)
            return {"cleared": True, "count": count}
        except redis.RedisError as e:
            logger.error("Clear escalations failed: %s", e)
            return {"cleared": False, "error": str(e)}

    # ── SLA scan ──────────────────────────────────────────────────────────────

    def scan_overdue(self) -> list[dict]:
        """
        Return all items past their SLA deadline without removing them.
        Called by monitoring/drift_detector.py.
        """
        try:
            now     = time.time()
            results = self._r.zrangebyscore(QUEUE_KEY, "-inf", now, withscores=True)
            return [json.loads(raw) for raw, _ in results]
        except redis.RedisError as e:
            logger.error("Scan overdue failed: %s", e)
            return []

```

---

### `backend\models\.gitkeep`

```

```

---

### `backend\models\__init__.py`

```python
# backend/models/__init__.py

```

---

### `backend\models\db.py`

```python
# backend/models/db.py
import os
from pathlib import Path
from sqlalchemy import (
    create_engine, Column, String, Integer, Float,
    Text, DateTime, Boolean, ForeignKey, event, UniqueConstraint
)
from sqlalchemy.orm import DeclarativeBase, sessionmaker, relationship
from datetime import datetime

# Ensure the database folder exists
os.makedirs("database", exist_ok=True)

DATABASE_URL = os.environ.get("DATABASE_URL", "sqlite:////app/database/project11.db")

# Auto-create the directory if using SQLite
if DATABASE_URL.startswith("sqlite:///"):
    db_path = DATABASE_URL.replace("sqlite:///", "")
    Path(db_path).parent.mkdir(parents=True, exist_ok=True)

engine = create_engine(
    DATABASE_URL,
    connect_args={"check_same_thread": False} if "sqlite" in DATABASE_URL else {},
    echo=False,
)

if "sqlite" in DATABASE_URL:
    @event.listens_for(engine, "connect")
    def set_sqlite_pragma(dbapi_conn, _):
        cursor = dbapi_conn.cursor()
        cursor.execute("PRAGMA journal_mode=WAL")
        cursor.execute("PRAGMA foreign_keys=ON")
        cursor.close()

SessionLocal = sessionmaker(autocommit=False, autoflush=False, bind=engine)

class Base(DeclarativeBase):
    pass


# ── Users ─────────────────────────────────────────────────────────────────────

class User(Base):
    __tablename__ = "users"
    __table_args__ = (
        UniqueConstraint("username", name="uq_users_username"),
        UniqueConstraint("email",    name="uq_users_email"),
    )

    id            = Column(String,      primary_key=True)
    username      = Column(String(64),  nullable=False, index=True)
    email         = Column(String(255), nullable=False, index=True)
    password_hash = Column(String(128), nullable=False)
    role          = Column(String(32),  nullable=False, default="analyst")
    is_active     = Column(Boolean,     default=True)
    created_at    = Column(DateTime,    default=datetime.utcnow)
    last_login    = Column(DateTime,    nullable=True)
    last_logout   = Column(DateTime,    nullable=True)
    # Soft delete — self-service account deletion sets this rather than
    # removing the row, so audit_log/plan_transactions/corrections history
    # (all FK'd to users.id) stays intact. is_active is also set to False
    # at the same time; deleted_at is what distinguishes "owner deactivated
    # this account" from "user deleted their own account".
    deleted_at    = Column(DateTime,    nullable=True)
    # Free-text region/location, e.g. "IN-MH" or "Mumbai, India" — not
    # geolocated automatically, set from whatever the signup/profile flow
    # collects (see hitl-go/routers/auth.go's CreateUser call site).
    region        = Column(String(128), nullable=True)
    # Running balance of field-extraction credits. Incremented by
    # plan_transactions.fields_allotted on a successful payment (see
    # routers/payment.go and payment_webhooks.go), decremented by 1 per
    # field per document actually queued for OCR (see
    # routers/documents.go's handleUploadOCR).
    token_balance = Column(Integer,     default=0, nullable=False)

    corrections = relationship(
        "Correction", back_populates="analyst",
        foreign_keys="Correction.analyst_id",
        primaryjoin="User.id == Correction.analyst_id",
    )


# ── Documents ─────────────────────────────────────────────────────────────────

class Document(Base):
    __tablename__ = "documents"

    doc_id        = Column(String,  primary_key=True)
    doc_hash      = Column(String,  index=True)
    filename      = Column(String)
    pdf_path      = Column(String)
    schema_path   = Column(String)
    status        = Column(String,  default="pending")
    doc_type      = Column(String)
    page_count    = Column(Integer)
    user_id       = Column(String,  ForeignKey("users.id"), index=True)
    extraction_id = Column(String,  index=True)
    error         = Column(Text)
    created_at    = Column(DateTime, default=datetime.utcnow)
    updated_at    = Column(DateTime, default=datetime.utcnow, onupdate=datetime.utcnow)

    extractions = relationship(
        "Extraction", back_populates="document",
        foreign_keys="Extraction.doc_id",
        primaryjoin="Document.doc_id == Extraction.doc_id",
    )


# ── Schema Registry ───────────────────────────────────────────────────────────

class SchemaRegistry(Base):
    __tablename__ = "schema_registry"

    id              = Column(Integer, primary_key=True, autoincrement=True)
    schema_id       = Column(String,  unique=True, nullable=False, index=True)
    schema_name     = Column(String,  nullable=False)
    version         = Column(String,  nullable=False)
    definition_json = Column(Text,    nullable=False)
    # FIX: was datetime.now() — evaluated once at import time (frozen timestamp)
    created_at      = Column(DateTime, default=datetime.utcnow)
    created_by      = Column(String,  ForeignKey("users.id"))


# ── Extraction Jobs (Celery pipeline) ─────────────────────────────────────────

class ExtractionJob(Base):
    __tablename__ = "extraction_jobs"

    extraction_id = Column(String,  primary_key=True)
    doc_hash      = Column(String,  index=True)
    schema_id     = Column(String,  index=True)
    pdf_path      = Column(String)
    status        = Column(String,  default="pending")
    result_json   = Column(Text)
    error         = Column(Text)
    # FIX: was datetime.now() — evaluated once at import time (frozen timestamp)
    created_at    = Column(DateTime, default=datetime.utcnow)
    started_at    = Column(DateTime, nullable=True)
    completed_at  = Column(DateTime, nullable=True)
    sla_deadline  = Column(DateTime, nullable=False,
                           default=datetime.utcnow)  # set properly on job creation


# ── Extractions (richer model) ────────────────────────────────────────────────

class Extraction(Base):
    __tablename__ = "extractions"

    extraction_id      = Column(String,  primary_key=True)
    doc_id             = Column(String,  ForeignKey("documents.doc_id"), index=True)
    schema_id          = Column(String,  ForeignKey("schema_registry.schema_id"), index=True)
    status             = Column(String,  default="pending")
    fields_json        = Column(Text)
    auto_accepted      = Column(Integer, default=0)
    queued_for_review  = Column(Integer, default=0)
    failed_count       = Column(Integer, default=0)
    overall_confidence = Column(Float)
    error              = Column(Text)
    created_at         = Column(DateTime, default=datetime.utcnow)
    started_at         = Column(DateTime, nullable=True)
    completed_at       = Column(DateTime, nullable=True)

    document = relationship(
        "Document", back_populates="extractions",
        foreign_keys=[doc_id],
        primaryjoin="Extraction.doc_id == Document.doc_id",
    )


# ── Corrections ───────────────────────────────────────────────────────────────

class Correction(Base):
    __tablename__ = "corrections"

    id                  = Column(String, primary_key=True)
    extraction_id       = Column(String, index=True)
    doc_hash            = Column(String, index=True)
    field_name          = Column(String, index=True)
    original_value      = Column(Text)
    corrected_value     = Column(Text)
    failure_type        = Column(String, index=True)
    analyst_id          = Column(String, ForeignKey("users.id"), index=True)
    schema_id           = Column(String, index=True)
    prompt_id           = Column(String, index=True)
    retrieved_chunk_ids = Column(Text)
    confidence_score    = Column(Float)
    context_relevance   = Column(Float)
    answer_relevance    = Column(Float)
    groundedness        = Column(Float)
    # FIX: was datetime.now() — evaluated once at import time (frozen timestamp)
    corrected_at        = Column(DateTime, default=datetime.utcnow)

    analyst = relationship(
        "User", back_populates="corrections",
        foreign_keys=[analyst_id],
        primaryjoin="Correction.analyst_id == User.id",
    )


# ── Audit Log ─────────────────────────────────────────────────────────────────

class AuditLog(Base):
    __tablename__ = "audit_log"

    id           = Column(Integer, primary_key=True, autoincrement=True)
    # FIX: nullable=True — middleware never sets this; unique still enforced when set
    entry_hash   = Column(String,  unique=True, index=True, nullable=True)
    path         = Column(String)
    method       = Column(String(8))
    user_id      = Column(String,  index=True)
    query_params = Column(Text,    nullable=True)
    duration_ms  = Column(Integer)
    status_code  = Column(Integer)
    # FIX: renamed from request_hash → body_hash to match middleware.py
    body_hash    = Column(String)
    timestamp    = Column(DateTime, default=datetime.utcnow, index=True)


# ── Chunks ────────────────────────────────────────────────────────────────────

class Chunk(Base):
    __tablename__ = "chunks"

    point_id   = Column(String,  primary_key=True)
    doc_id     = Column(String,  ForeignKey("documents.doc_id"), index=True)
    doc_hash   = Column(String,  index=True)
    page_num   = Column(Integer)
    section    = Column(String)
    chunk_type = Column(String)
    table_type = Column(String)
    text       = Column(Text)
    created_at = Column(DateTime, default=datetime.utcnow)


# ── Prompt Versions ───────────────────────────────────────────────────────────

class PromptVersion(Base):
    __tablename__ = "prompt_versions"

    prompt_id   = Column(String,  primary_key=True)
    prompt_name = Column(String,  index=True)
    version_tag = Column(String)
    body        = Column(Text)
    created_at  = Column(DateTime, default=datetime.utcnow)
    created_by  = Column(String,  ForeignKey("users.id"))
    is_active   = Column(Boolean, default=True)


# ── Helpers ───────────────────────────────────────────────────────────────────

class UploadSession(Base):
    """
    Tracks a folder upload session so uploads can be resumed after
    connection drops, browser refreshes, or internal errors.
    One session = one folder upload attempt.
    """
    __tablename__ = "upload_sessions"

    session_id    = Column(String,   primary_key=True)  # UUID, generated client-side
    user_id       = Column(String,   ForeignKey("users.id"), nullable=False)
    schema_id     = Column(String,   nullable=True)      # set after regions are saved
    total_files   = Column(Integer,  nullable=False)     # total PDFs in the folder
    queued_count  = Column(Integer,  default=0)          # successfully queued so far
    skipped_count = Column(Integer,  default=0)          # oversized/invalid files
    failed_count  = Column(Integer,  default=0)          # files that errored
    status        = Column(String,   default="in_progress")  # in_progress|complete|interrupted
    created_at    = Column(DateTime, default=datetime.utcnow)
    updated_at    = Column(DateTime, default=datetime.utcnow, onupdate=datetime.utcnow)

    files = relationship("UploadSessionFile", back_populates="session",
                         cascade="all, delete-orphan")


class UploadSessionFile(Base):
    """
    One row per PDF file in an upload session.
    doc_hash is used for deduplication on resume — if a file with the
    same hash is already queued, it is skipped on re-upload.
    """
    __tablename__ = "upload_session_files"

    id            = Column(String,   primary_key=True)   # UUID
    session_id    = Column(String,   ForeignKey("upload_sessions.session_id"), nullable=False)
    filename      = Column(String,   nullable=False)
    doc_hash      = Column(String,   nullable=True)       # sha256 of file content
    file_index    = Column(Integer,  nullable=False)      # 0-based position in folder
    status        = Column(String,   default="pending")   # pending|queued|skipped|failed
    extraction_id = Column(String,   nullable=True)       # set when job is created
    error         = Column(Text,     nullable=True)
    size_bytes    = Column(Integer,  nullable=True)
    processed_at  = Column(DateTime, nullable=True)

    session = relationship("UploadSession", back_populates="files")


def init_db():
    Base.metadata.create_all(bind=engine)


def get_db():
    db = SessionLocal()
    try:
        yield db
    finally:
        db.close()

```

---

### `backend\models\db_backup_20260723_203132.json`

```json
{
  "audit_log": [
    {
      "duration_ms": 0,
      "id": 207,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:31:26.269979Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 206,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:31:16.160381Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 205,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:31:06.068452Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 204,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:30:55.97174Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 203,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:30:44.595732Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 202,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:30:34.47274Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 201,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:30:24.367344Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 200,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:30:14.253486Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 199,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:30:04.136417Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 198,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:29:54.035645Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 197,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:29:43.927468Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 196,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:29:33.827182Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 195,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:29:23.701821Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 194,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:29:13.605024Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 193,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:29:03.505952Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 192,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:28:53.422517Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 191,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:28:43.321955Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 190,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:28:33.230235Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 189,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:28:23.106278Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 188,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:28:13.013761Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 187,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:28:02.90313Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 186,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:27:52.784618Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 185,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:27:42.679701Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 184,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:27:32.58956Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 183,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:27:22.483371Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 182,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:27:12.373165Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 181,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:27:02.260101Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 180,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:26:52.168513Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 179,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:26:42.072197Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 178,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:26:31.950335Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 177,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:26:21.841722Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 176,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:26:11.723951Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 175,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:26:01.625665Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 174,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:25:51.511874Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 173,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:25:41.380782Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 172,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-23T20:25:31.260483Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 149,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T15:08:16.448093Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 148,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T15:08:06.348975Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 147,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T15:07:56.23881Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 146,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T15:07:46.132922Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 145,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T15:07:36.024108Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 144,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T15:07:25.921081Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 143,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T15:07:15.849848Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 142,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T15:07:05.756945Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 141,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T15:06:55.65016Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 140,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T15:06:45.548442Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 139,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T15:06:35.468563Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 138,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:12:19.806589Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 137,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:12:09.712547Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 136,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:11:59.63396Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 135,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:11:49.54722Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 134,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:11:39.451032Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 133,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:11:29.346649Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 132,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:11:19.243585Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 131,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:11:09.147129Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 130,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:10:59.066578Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 129,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:10:48.975738Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 128,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:10:38.877315Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 127,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:10:28.803971Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 126,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:10:18.713129Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 125,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:10:08.616593Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 124,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:09:58.529753Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 123,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:09:48.434393Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 122,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:09:38.352055Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 121,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:09:28.255209Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 120,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:09:18.143065Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 119,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:09:08.043911Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 118,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:08:57.942992Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 117,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:08:47.835683Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 116,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:08:37.744132Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 115,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:08:27.630384Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 114,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:08:17.519282Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 113,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:08:07.400394Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 112,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:07:57.2952Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 111,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:07:47.189054Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 110,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:07:37.092759Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 109,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:07:26.997313Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 108,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:07:16.912084Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 107,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:07:06.812209Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 106,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:06:56.731679Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 105,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:06:46.630259Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 104,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:06:36.523011Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 103,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:06:26.433881Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 102,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:06:16.321559Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 101,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:06:06.222745Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 100,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:05:56.122682Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 99,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:05:46.010493Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 98,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:05:35.919207Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 97,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:05:25.815041Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 96,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:05:15.72054Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 95,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:05:05.636216Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 94,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:04:55.517885Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 93,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:04:45.416193Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 92,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:04:35.317617Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 91,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:04:25.234006Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 90,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:04:15.101854Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 89,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:04:05.011478Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 88,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:03:54.907707Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 87,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:03:44.819674Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 86,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:03:34.729322Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 85,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:03:24.630553Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 84,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:03:14.539445Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 83,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:03:04.447875Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 82,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T13:02:54.371423Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 81,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:51:27.134158Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 80,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:51:17.023798Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 79,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:51:06.931829Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 78,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:50:56.834003Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 77,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:50:46.741933Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 76,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:50:36.643174Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 75,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:50:26.539394Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 74,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:50:16.425693Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 73,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:50:06.325053Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 72,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:49:56.237784Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 71,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:49:46.132485Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 70,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:49:36.045554Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 69,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:49:25.964795Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 68,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:49:15.890996Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 67,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:49:05.795493Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 66,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:48:55.677835Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 65,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:48:45.584862Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 64,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:48:35.497681Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 63,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-22T12:48:25.416303Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 34,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:24:51.293671Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 33,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:24:41.207912Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 32,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:24:31.098535Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 31,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:24:20.998213Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 30,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:24:10.902661Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 29,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:24:00.802709Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 28,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:23:50.694735Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 27,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:23:40.604305Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 26,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:23:30.520845Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 25,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:23:20.426615Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 24,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:23:10.319602Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 23,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:23:00.210255Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 22,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:22:50.118846Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 21,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:22:40.00066Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 20,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:22:29.908133Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 19,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:22:19.821589Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 18,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:22:09.728084Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 17,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:21:59.613831Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 16,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:21:49.516055Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 15,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:21:39.413526Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 14,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:21:29.327858Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 13,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:21:19.250502Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 12,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:21:09.152434Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 11,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:20:59.041743Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 10,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:20:48.927527Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 9,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:20:38.825929Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 8,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:20:28.740333Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 7,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:20:18.651697Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 6,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:20:08.564374Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 5,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:19:58.478411Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 4,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:19:48.386203Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 3,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:19:38.297289Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 2,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:19:28.199397Z",
      "user_id": "anonymous"
    },
    {
      "duration_ms": 0,
      "id": 1,
      "method": "GET",
      "path": "/health",
      "status_code": 200,
      "timestamp": "2026-07-18T10:19:18.120511Z",
      "user_id": "anonymous"
    }
  ],
  "backed_up_at": "2026-07-23T20:31:32Z",
  "corrections": null,
  "counts": {
    "audit_log": 157,
    "corrections": 0,
    "documents": 0,
    "extraction_jobs": 0,
    "extractions": 0,
    "prompt_versions": 0,
    "schema_registry": 0,
    "users": 0
  },
  "documents": null,
  "extraction_jobs": null,
  "extractions": null,
  "prompt_versions": null,
  "schema_registry": null,
  "users": null
}

```

---

### `backend\monitoring\.gitkeep`

```

```

---

### `backend\monitoring\__init__.py`

```python
# backend/monitoring/__init__.py

```

---

### `backend\monitoring\drift_detector.py`

```python
# backend/monitoring/drift_detector.py
"""
Field-level accuracy drift detector.
Compares recent confidence scores (last 7 days) vs baseline (prior 7 days).
Fires alerts when a field's accuracy drops more than DRIFT_THRESHOLD.
"""
import logging
import os
from datetime import datetime, timedelta

from models.db import SessionLocal, Correction

logger          = logging.getLogger("drift_detector")
DRIFT_THRESHOLD = float(os.getenv("DRIFT_THRESHOLD", "0.1"))  # 10% drop triggers alert


class DriftDetector:

    def compute_drift_report(self) -> list[dict]:
        """
        Returns a list of per-field drift entries.
        Each entry has: field_name, recent_avg, baseline_avg, drift, alert.
        """
        db  = SessionLocal()
        now = datetime.utcnow()
        try:
            recent_start   = now - timedelta(days=7)
            baseline_start = now - timedelta(days=14)
            baseline_end   = now - timedelta(days=7)

            # Get all field names that have enough corrections
            fields = (
                db.query(Correction.field_name)
                .filter(Correction.confidence_score.isnot(None))
                .group_by(Correction.field_name)
                .having(db.query(Correction).filter(
                    Correction.field_name == Correction.field_name
                ).count() >= 5)
                .all()
            )

            report = []
            for (field_name,) in fields:
                recent_scores = [
                    c.confidence_score
                    for c in db.query(Correction)
                    .filter(
                        Correction.field_name == field_name,
                        Correction.corrected_at >= recent_start,
                        Correction.confidence_score.isnot(None),
                    ).all()
                ]
                baseline_scores = [
                    c.confidence_score
                    for c in db.query(Correction)
                    .filter(
                        Correction.field_name == field_name,
                        Correction.corrected_at >= baseline_start,
                        Correction.corrected_at < baseline_end,
                        Correction.confidence_score.isnot(None),
                    ).all()
                ]

                if not recent_scores or not baseline_scores:
                    continue

                recent_avg   = sum(recent_scores)   / len(recent_scores)
                baseline_avg = sum(baseline_scores) / len(baseline_scores)
                drift        = baseline_avg - recent_avg   # positive = degraded
                alert        = drift > DRIFT_THRESHOLD

                if alert:
                    logger.warning(
                        "Drift alert | field=%s baseline=%.3f recent=%.3f drift=%.3f",
                        field_name, baseline_avg, recent_avg, drift,
                    )

                report.append({
                    "field_name":   field_name,
                    "recent_avg":   round(recent_avg,   4),
                    "baseline_avg": round(baseline_avg, 4),
                    "drift":        round(drift,        4),
                    "alert":        alert,
                    "recent_n":     len(recent_scores),
                    "baseline_n":   len(baseline_scores),
                })

            return sorted(report, key=lambda x: x["drift"], reverse=True)

        except Exception as e:
            logger.error("Drift computation failed: %s", e)
            return []
        finally:
            db.close()

    def get_alerts(self) -> list[dict]:
        """Return only fields with active drift alerts."""
        return [e for e in self.compute_drift_report() if e["alert"]]

```

---

### `backend\requirements.txt`

```
# backend/requirements.txt
# Python worker dependencies only.
# No FastAPI, no uvicorn — HTTP is handled by the Go server.

# ── Database ──────────────────────────────────────────────────────────────────
sqlalchemy>=2.0.0
psycopg2-binary>=2.9.0

# ── Task queue ────────────────────────────────────────────────────────────────
celery[redis]>=5.4.0
redis>=5.0.0

# ── ML / LLM ──────────────────────────────────────────────────────────────────
anthropic>=0.28.0
groq>=0.9.0

# ── PDF parsing ───────────────────────────────────────────────────────────────
pdfplumber>=0.11.0
pytesseract>=0.3.10
Pillow>=10.0.0

# ── Vector store ──────────────────────────────────────────────────────────────
# NOTE: sentence-transformers (and its torch dependency) is installed
# separately in the Dockerfile, in its own cached layer, BEFORE this file
# is even copied in. Do not re-list it here — that would just cause pip
# to redundantly re-resolve it on every requirements.txt change.
qdrant-client>=1.9.0

# ── Utilities ─────────────────────────────────────────────────────────────────
python-dotenv>=1.0.0
pydantic>=2.0.0          # services/schema_service.py — FieldDefinition/SchemaDefinition models

# ── PDF correction export ──────────────────────────────────────────────────────
PyMuPDF>=1.24.0          # fitz — used by apply_correction_to_pdf to edit PDF pages
openpyxl>=3.1.0          # corrections.xlsx export

```

---

### `backend\services\__init__.py`

```python
# backend/utils/__init__.py

```

---

### `backend\services\confidence_gate.py`

```python
# backend/services/confidence_gate.py
from enum import Enum
from hitl.sla_queue import SLAQueue
from services.reextractor import ReExtractor

class GateDecision(str, Enum):
    AUTO_ACCEPT  = "auto_accept"
    HUMAN_REVIEW = "human_review"
    RE_EXTRACT   = "re_extract"
    FAIL = "fail"

class ConfidenceGate:
    """
    Implements the Confidence Gate from the mind map:
    Score > 0.85 → auto-accept → output DB
    Score 0.5–0.85 → SLA queue → human review
    Score < 0.5 → re-extract (strategy rotation)

    This is the most critical decision node in the system.
    Too low threshold = analyst overwhelmed with reviews.
    Too high threshold = hallucinations accepted automatically.
    0.85 is the starting point — tune based on domain risk tolerance.
    """

    HIGH = 0.85
    LOW  = 0.50

    def __init__(self, db):
        self.queue      = SLAQueue()
        self.reextract  = ReExtractor(db)

    def _enqueue_for_review(self, extraction_id: str, extraction_result: dict,
                             field_name: str, priority: int, sla_minutes: int) -> dict:
        """
        Adapter around SLAQueue.push() — that's the method that actually
        exists on SLAQueue (backend/hitl/sla_queue.py); there is no
        enqueue() method there. push() takes a flat item dict and
        sla_hours (not sla_minutes), so this builds that shape and does
        the unit conversion.
        """
        item = {
            "item_id":       f"{extraction_id}:{field_name}",
            "extraction_id": extraction_id,
            "field_name":    field_name,
            "priority":      priority,
            "result":        extraction_result,
        }
        sla_hours = max(sla_minutes / 60, 1 / 60)  # push() rounds to hours; never pass 0
        self.queue.push(item, sla_hours=sla_hours)
        return item

    def decide(self, extraction_id:str,extraction_result: dict, eval_scores: dict,
               field, doc_hash: str, schema_service) -> dict:
        """
        Takes extraction result + evaluation scores.
        Returns decision + next action.
        """
        score = eval_scores["confidence_score"]
        merged = {**extraction_result, **eval_scores}

        if score >= self.HIGH:
            return {
                "decision":   GateDecision.AUTO_ACCEPT,
                "score":      score,
                "result":     merged,
                "accepted":   True,
            }

        elif score >= self.LOW:
            queue_item = self._enqueue_for_review(
                extraction_id =extraction_id,
                extraction_result = merged,
                field_name        = field.name,
                priority          = self._priority(score),
                sla_minutes       = 60,
            )
            return {
                "decision":   GateDecision.HUMAN_REVIEW,
                "score":      score,
                "queue_item": queue_item,
                "accepted":   False,
            }

        else:
            queue_item = self._enqueue_for_review(
                extraction_id =extraction_id,
                extraction_result=merged,
                field_name=field.name,
                priority=self._priority(score),
                sla_minutes=60,
            )
            retry = self.reextract.retry(
                field          = field,
                doc_hash       = doc_hash,
                schema_service = schema_service,
                attempt        = 1,

            )
            if retry.get("status") == "EXTRACTION_FAILED":
                return {
                    "decision": GateDecision.FAIL,
                    "score": score,
                    "retry": retry,
                    "queue_item": queue_item,
                    "accepted": False,
                }
            return {
                "decision": GateDecision.RE_EXTRACT,
                "score":    score,
                "retry":    retry,
                "queue_item": queue_item,
                "accepted": False,
            }

    def _priority(self, score: float) -> int:
        """Lower confidence = higher urgency = higher priority score."""
        return int((1.0 - score) * 100)
```

---

### `backend\services\evaluator.py`

```python
# backend/services/evaluator.py
import re
import os
from groq import Groq

class RAGEvaluator:
    WEIGHTS = {
        "context_relevance":  0.35,
        "groundedness":       0.40,
        "answer_relevance":   0.25,
    }

    def __init__(self):
        self.client = Groq(api_key=os.environ.get("GROQ_API"))

    def evaluate(self, field_name: str, field_type: str,
                 extracted_value: str, retrieved_texts: list[str],
                 query: str) -> dict:
        from utils.timing import timed

        if extracted_value in ["EXTRACTION_FAILED", ""]:
            return {
                "context_relevance": 0.0,
                "groundedness":      0.0,
                "answer_relevance":  0.0,
                "confidence_score":  0.0,
            }

        # Each of these is a separate LLM call — timed individually
        with timed("evaluation", "context_relevance", field_name=field_name):
            cr = self._context_relevance(query, retrieved_texts)

        with timed("evaluation", "groundedness", field_name=field_name):
            gnd = self._groundedness(extracted_value, retrieved_texts)

        with timed("evaluation", "answer_relevance", field_name=field_name):
            ar = self._answer_relevance(field_name, field_type, extracted_value)

        confidence = (
            self.WEIGHTS["context_relevance"] * cr +
            self.WEIGHTS["groundedness"]      * gnd +
            self.WEIGHTS["answer_relevance"]  * ar
        )

        return {
            "context_relevance": round(cr, 3),
            "groundedness":      round(gnd, 3),
            "answer_relevance":  round(ar, 3),
            "confidence_score":  round(confidence, 3),
        }

    def _context_relevance(self, query: str, chunks: list[str]) -> float:
        context_sample = "\n".join(chunks[:2])[:600]
        prompt = f"""Score 0.0 to 1.0: how relevant is this context to finding the value for '{query}'?

Context: {context_sample}

0.0 = completely unrelated  |  1.0 = directly contains the answer
Return ONLY a decimal number."""
        return self._llm_score(prompt, default=0.5)

    def _groundedness(self, value: str, chunks: list[str]) -> float:
        if value in ["NOT FOUND", "N/A", "EXTRACTION_FAILED"]:
            return 0.0

        context = " ".join(chunks)

        # Fast paths — no LLM needed
        if value.strip() in context:
            return 1.0
        clean_val = re.sub(r"[$,%\s]", "", value)
        clean_ctx = re.sub(r"[$,%\s]", "", context)
        if clean_val and clean_val in clean_ctx:
            return 0.95

        # LLM judge only when fast paths miss
        prompt = f"""Is the extracted value '{value}' supported by any of this context?
Consider numeric equivalents ($4.5M = 4,500,000), abbreviations, and formatting differences.

Context: {context[:800]}

0.0 = not supported (hallucination)  |  1.0 = clearly supported
Return ONLY a decimal number."""
        return self._llm_score(prompt, default=0.3)

    def _answer_relevance(self, field_name: str, field_type: str, value: str) -> float:
        prompt = f"""Score 0.0 to 1.0: does '{value}' make sense as the value for field '{field_name}' (type: {field_type})?

Examples of good answers:
- currency field: $4.5M, 4,500,000, $1.2B
- date field: 2024-12-31, December 31, 2024
- percentage field: 15.3%, 0.153
- ein field: 12-3456789

0.0 = wrong type/format  |  1.0 = correct type and plausible value
Return ONLY a decimal number."""
        return self._llm_score(prompt, default=0.5)

    def _llm_score(self, prompt: str, default: float) -> float:
        try:
            answer = self.client.chat.completions.create(
                messages=[{"role": "user", "content": prompt}],
                model="llama-3.3-70b-versatile",
                max_tokens=10,
            )
            text = answer.choices[0].message.content.strip()
            return max(0.0, min(1.0, float(text)))
        except Exception:
            return default

```

---

### `backend\services\extractor.py`

```python
# backend/services/extractor.py
import os
import re
from typing import Dict, Any
from groq import Groq
from services.vector_store import VectorStoreService
from services.prompt_registry import PromptRegistryService
from services.schema_service import FieldDefinition, SchemaServiceImpl

class FieldExtractor:
    def __init__(self, db):
        self.client  = Groq(api_key=os.environ.get("GROQ_API"))
        self.vs      = VectorStoreService()
        self.prompts = PromptRegistryService(db)

    def extract_field(
        self,
        field: FieldDefinition,
        doc_hash: str,
        schema_id: str,
        schema_service: SchemaServiceImpl,
        top_k: int = 3,
    ) -> Dict[str, Any]:
        from utils.timing import timed

        query = schema_service.build_query(field)

        # Step 1: Vector retrieval
        with timed("extraction", "retrieve_chunks", field_name=field.name,
                   meta={"top_k": top_k}):
            chunks = self.vs.retrieve(query, doc_hash, top_k=top_k)

        if not chunks:
            return self._failed_result(field, schema_id, "no_chunks_retrieved", [])

        context = self._format_context(chunks)

        # Step 2: Prompt construction
        with timed("extraction", "build_prompt", field_name=field.name):
            prompt_id, template = self.prompts.get_active("field_extraction")
            prompt = self.prompts.format_prompt(template, field, context)

        # Step 3: LLM call
        with timed("extraction", "llm_call", field_name=field.name,
                   meta={"model": "llama-3.3-70b-versatile"}):
            try:
                answer = self.client.chat.completions.create(
                    messages=[{"role": "user", "content": prompt}],
                    model="llama-3.3-70b-versatile",
                    max_tokens=256,
                )
                raw_output = answer.choices[0].message.content
            except Exception as e:
                return self._failed_result(field, schema_id, f"llm_error: {e}", [])

        # Step 4: Parse + validate
        with timed("extraction", "parse_validate", field_name=field.name):
            extracted_value = self._parse_output(raw_output)
            is_valid, guard_reason = schema_service.validate_value(extracted_value, field)

        return {
            "field_name":           field.name,
            "field_type":           field.field_type.value,
            "raw_value":            extracted_value,
            "is_valid":             is_valid,
            "guard_reason":         guard_reason,
            "retrieved_chunk_ids":  [c["point_id"] for c in chunks],
            "retrieved_texts":      [c["text"] for c in chunks],
            "retrieval_scores":     [c["retrieval_score"] for c in chunks],
            "prompt_id":            prompt_id,
            "schema_id":            schema_id,
            "raw_llm_output":       raw_output,
        }

    def _format_context(self, chunks: list) -> str:
        parts = []
        for i, chunk in enumerate(chunks, 1):
            page    = chunk.get("page_num", "?")
            section = chunk.get("section", "")
            parts.append(f"[Context {i} — Page {page}, Section: {section}]\n{chunk['text']}")
        return "\n\n---\n\n".join(parts)

    def _parse_output(self, raw: str) -> str:
        text  = re.sub(r"<think>.*?</think>", "", raw, flags=re.DOTALL).strip()
        match = re.search(r"<FINAL_VALUE>(.*?)</FINAL_VALUE>", text, re.DOTALL)
        if match:
            value = match.group(1).strip()
            return value if len(value) <= 300 else "EXTRACTION_FAILED"
        lines = [l.strip() for l in text.splitlines() if l.strip()]
        if not lines:
            return "EXTRACTION_FAILED"
        candidate = lines[-1]
        if len(candidate) > 300:
            return "EXTRACTION_FAILED"
        return candidate

    def _failed_result(self, field, schema_id, reason, chunk_ids) -> Dict[str, Any]:
        return {
            "field_name":          field.name,
            "field_type":          field.field_type.value,
            "raw_value":           "EXTRACTION_FAILED",
            "is_valid":            False,
            "guard_reason":        reason,
            "retrieved_chunk_ids": chunk_ids,
            "retrieved_texts":     [],
            "retrieval_scores":    [],
            "prompt_id":           "none",
            "schema_id":           schema_id,
            "raw_llm_output":      "",
        }

```

---

### `backend\services\normalizer.py`

```python
 # backend/services/normalizer.py
import re, hashlib
from typing import List, Dict, Any
from services.table_tagger import TableTagger

class Normalizer:
    """
    Implements 'Content Normalization Layer' from the mind map:
    - Header-aware serialization (section detection)
    - Table type tagging (via TableTagger)
    - Section-aware chunking (split on semantic sections, not char count)
    - Metadata enrichment (page_num, section, chunk_type, doc_hash per chunk)

    The key insight from Codebasics:
    NAIVE:  Split every 500 characters → breaks financial context
    BETTER: Split by page boundary → preserves context but coarse
    BEST:   Split by semantic section (header-aware chunking) → used here
    """

    SECTION_HEADER_PATTERN = re.compile(
        r"^(#{1,6}\s+.+|[A-Z][A-Z\s]{4,50}$|\d+\.\s+[A-Z].+|"
        r"ITEM\s+\d+|NOTE\s+\d+|PART\s+[IVX]+)",
        re.MULTILINE
    )

    def __init__(self):
        self.tagger = TableTagger()

    def chunk_document(self, parsed_doc: dict) -> List[Dict[str, Any]]:
        """
        Master chunking function. Returns list of chunks ready for embedding.
        Each chunk has full metadata for retrieval and provenance.
        """
        chunks = []
        for page in parsed_doc["pages"]:
            # Chunk 1: prose sections from page text
            prose_chunks = self._chunk_prose(page)
            chunks.extend(prose_chunks)

            # Chunk 2: tables from page (indexed separately)
            # Key insight from Codebasics: tables have different semantic density
            # than prose → index separately for better retrieval
            table_chunks = self._chunk_tables(page)
            chunks.extend(table_chunks)

        return chunks

    def _chunk_prose(self, page: dict) -> List[Dict[str, Any]]:
        """Split prose text on detected section headers."""
        text = page.get("text", "")
        if not text.strip():
            return []

        # Find section header positions
        splits = [(m.start(), m.group()) for m in self.SECTION_HEADER_PATTERN.finditer(text)]

        if not splits:
            # No headers detected — treat whole page as one chunk
            return [self._make_chunk(
                text=text.strip(),
                page_num=page["page_num"],
                section="page_" + str(page["page_num"]),
                chunk_type="prose",
                doc_hash=page["doc_hash"],
            )]

        # Split into sections
        chunks = []
        for i, (pos, header) in enumerate(splits):
            end = splits[i+1][0] if i+1 < len(splits) else len(text)
            section_text = text[pos:end].strip()
            if len(section_text) > 50:  # skip trivially short sections
                chunks.append(self._make_chunk(
                    text=section_text,
                    page_num=page["page_num"],
                    section=header.strip()[:80],
                    chunk_type="prose",
                    doc_hash=page["doc_hash"],
                ))
        return chunks

    def _chunk_tables(self, page: dict) -> List[Dict[str, Any]]:
        """Process and serialize each table on the page."""
        chunks = []
        for table_idx, table_rows in enumerate(page.get("tables", [])):
            if not table_rows:
                continue

            headers, data_rows = self.tagger.get_column_headers(table_rows)
            surrounding = page.get("text", "")[:300]  # context for classification
            table_type  = self.tagger.classify(table_rows, surrounding)
            serialized  = self.tagger.serialize(data_rows, headers, table_type)

            if serialized.strip():
                chunks.append(self._make_chunk(
                    text=serialized,
                    page_num=page["page_num"],
                    section=f"table_{table_idx+1}_page_{page['page_num']}",
                    chunk_type="table",
                    doc_hash=page["doc_hash"],
                    table_type=table_type.value,
                    headers=headers,
                ))
        return chunks

    def _make_chunk(self, text: str, page_num: int, section: str,
                    chunk_type: str, doc_hash: str, **extra) -> Dict[str, Any]:
        """
        Metadata enrichment: every chunk carries full provenance.
        chunk_id is deterministic hash — same content = same ID.
        """
        chunk_id = hashlib.sha256(f"{doc_hash}:{section}:{text[:100]}".encode()).hexdigest()[:16]
        return {
            "chunk_id":   chunk_id,
            "text":       text,
            "page_num":   page_num,
            "section":    section,
            "chunk_type": chunk_type,
            "doc_hash":   doc_hash,
            **extra
        }
```

---

### `backend\services\ocr_extractor.py`

```python
# backend/services/ocr_extractor.py
#
# Direct region-based extraction for mode="ocr" schemas (see
# schema_service.py's SchemaDefinition.mode). This file makes NO network
# calls, imports no LLM SDK, and does not touch the vector store — that's
# the entire point of it existing separately from services/extractor.py,
# which is the RAG/LLM path.
#
# Two-tier strategy per field:
#   1. If the PDF page has a real text layer (digital PDF), crop to the
#      drawn region's bounding box and read text directly via pdfplumber.
#      This is exact — no OCR error risk at all for digital documents.
#   2. If that comes back empty (scanned/image-only page), render just
#      that cropped region to an image and run Tesseract OCR on it —
#      still fully local, still no LLM, no embeddings, no network call.
import logging

import pdfplumber
import pytesseract

logger = logging.getLogger("ocr_extractor")

# Region coordinates come from PDFRegionEditor.tsx as PDF points measured
# from the top-left of the page — pdfplumber's crop()/within_bbox() use the
# same (x0, top, x1, bottom) top-left-origin convention, so no coordinate
# flip is needed here. If that ever changes on the frontend, this is the
# file to revisit.
TESSERACT_IMAGE_DPI = 300  # higher DPI = better OCR accuracy, slower render


class RegionOCRExtractor:
    def extract_field(self, field, pdf_path: str) -> dict:
        """
        field is a schema_service.FieldDefinition with .region set to
        {"page": int, "x": float, "y": float, "w": float, "h": float}
        (1-indexed page, PDF points). Returns a dict shaped to match what
        services/extractor.py's FieldExtractor.extract_field returns, so
        workers/celery_app.py can treat both extraction paths uniformly
        when building field_results.
        """
        region = field.region
        if not region:
            return self._failed_result(field, "no_region_defined")

        try:
            page_num = int(region["page"])
            x, y, w, h = float(region["x"]), float(region["y"]), float(region["w"]), float(region["h"])
        except (KeyError, TypeError, ValueError) as e:
            return self._failed_result(field, f"invalid_region: {e}")

        try:
            with pdfplumber.open(pdf_path) as pdf:
                if page_num < 1 or page_num > len(pdf.pages):
                    return self._failed_result(field, f"page_{page_num}_out_of_range")
                page = pdf.pages[page_num - 1]

                bbox = (x, y, x + w, y + h)
                # Clamp to page bounds — a region drawn slightly outside the
                # page edge (off-by-a-pixel in the editor) shouldn't crash
                # the whole extraction.
                bbox = (
                    max(0, bbox[0]), max(0, bbox[1]),
                    min(page.width, bbox[2]), min(page.height, bbox[3]),
                )
                if bbox[2] <= bbox[0] or bbox[3] <= bbox[1]:
                    return self._failed_result(field, "region_outside_page_bounds")

                cropped = page.crop(bbox)

                # Tier 1 — direct text layer, exact, no OCR guessing at all.
                text = (cropped.extract_text() or "").strip()
                if text:
                    return self._ok_result(field, text, source="text_layer")

                # Tier 2 — no text layer in this region (scanned page or a
                # region that's genuinely an image) — render just this crop
                # to an image and OCR it locally via Tesseract.
                im = cropped.to_image(resolution=TESSERACT_IMAGE_DPI).original
                ocr_text = pytesseract.image_to_string(im).strip()
                if ocr_text:
                    return self._ok_result(field, ocr_text, source="tesseract")

                return self._failed_result(field, "no_text_found_in_region")

        except Exception as e:
            logger.error("OCR extraction failed for field %s: %s", field.name, e)
            return self._failed_result(field, f"ocr_error: {e}")

    def _ok_result(self, field, value: str, source: str) -> dict:
        return {
            "field_name":          field.name,
            "field_type":          field.field_type.value,
            "raw_value":           value,
            "is_valid":            True,
            "guard_reason":        f"ocr_{source}",
            "retrieved_chunk_ids": [],
            "retrieved_texts":     [value],
            "retrieval_scores":    [],
            "prompt_id":           "none",
            "schema_id":           "",
            "raw_llm_output":      "",
        }

    def _failed_result(self, field, reason: str) -> dict:
        return {
            "field_name":          field.name,
            "field_type":          field.field_type.value,
            "raw_value":           "EXTRACTION_FAILED",
            "is_valid":            False,
            "guard_reason":        reason,
            "retrieved_chunk_ids": [],
            "retrieved_texts":     [],
            "retrieval_scores":    [],
            "prompt_id":           "none",
            "schema_id":           "",
            "raw_llm_output":      "",
        }
```

---

### `backend\services\parser_service.py`

```python
# backend/services/parser_service.py
import os
import pdfplumber
from enum import Enum
from typing import List, Dict, Any

class DocType(str, Enum):
    DIGITAL = "digital"   # clean text layer
    SCANNED = "scanned"   # image-only, needs OCR
    MIXED   = "mixed"     # some digital, some scanned pages

class ParserService:
    """
    Implements the Document Processing Pipeline from the mind map.

    Two strategies:
    1. Rule-based (deterministic): pdfplumber for digital PDFs
       → Use when: consistent, template-based documents
    2. ML-based (probabilistic): AWS Textract for scanned docs
       → Use when: scanned, variable-layout, handwritten

    This is exactly the decision tree from the Codebasics slide:
    "Rule-based (pdfplumber, PyMuPDF) vs ML-based (layoutparser, Textract)"
    """

    DIGITAL_TEXT_THRESHOLD = 100  # chars on first page to classify as digital

    def detect_doc_type(self, pdf_path: str) -> DocType:
        """Route to cheap rule-based or expensive OCR based on text layer."""
        with pdfplumber.open(pdf_path) as pdf:
            first_page_text = pdf.pages[0].extract_text() or ""
        if len(first_page_text.strip()) > self.DIGITAL_TEXT_THRESHOLD:
            return DocType.DIGITAL
        return DocType.SCANNED

    def parse(self, pdf_path: str, doc_hash: str) -> Dict[str, Any]:
        """Main entry point. Returns structured parsed document."""
        doc_type = self.detect_doc_type(pdf_path)
        if doc_type == DocType.DIGITAL:
            return self._parse_digital(pdf_path, doc_hash)
        else:
            return self._parse_with_ocr(pdf_path, doc_hash)

    def _parse_digital(self, pdf_path: str, doc_hash: str) -> Dict[str, Any]:
        """
        pdfplumber extracts:
        - Text per page (preserving layout)
        - Tables as list of rows (raw, before serialization)
        - Bounding boxes for chunk provenance

        Key insight: extract_text(layout=True) preserves column
        alignment in financial statements — critical for table parsing.
        """
        pages = []
        with pdfplumber.open(pdf_path) as pdf:
            for i, page in enumerate(pdf.pages):
                text   = page.extract_text(layout=True) or ""
                tables = page.extract_tables() or []
                pages.append({
                    "page_num": i + 1,
                    "text":     text,
                    "tables":   tables,
                    "width":    float(page.width),
                    "height":   float(page.height),
                    "doc_hash": doc_hash,
                })
        return {"doc_type": DocType.DIGITAL, "pages": pages, "total_pages": len(pages)}

    def _parse_with_ocr(self, pdf_path: str, doc_hash: str) -> Dict[str, Any]:
        """
        AWS Textract OCR for scanned PDFs.
        Falls back gracefully with empty pages if Textract unavailable.
        """
        try:
            import boto3
            textract = boto3.client("textract", region_name="us-east-1")
            with open(pdf_path, "rb") as f:
                raw = f.read()
            response = textract.detect_document_text(Document={"Bytes": raw})
            lines = [
                b["Text"]
                for b in response.get("Blocks", [])
                if b["BlockType"] == "LINE"
            ]
            full_text = "\n".join(lines)
            return {
                "doc_type": DocType.SCANNED,
                "pages": [{"page_num": 1, "text": full_text, "tables": [], "doc_hash": doc_hash}],
                "total_pages": 1,
            }
        except Exception as e:
            # Graceful degradation — return empty rather than crash
            return {
                "doc_type": DocType.SCANNED,
                "pages": [{"page_num": 1, "text": "", "tables": [], "doc_hash": doc_hash}],
                "total_pages": 1,
                "error": str(e),
            }
```

---

### `backend\services\prompt_registry.py`

```python
# backend/services/prompt_registry_service.py
import hashlib
from sqlalchemy.orm import Session
from models.db import PromptVersion

# Default extraction prompt templates
DEFAULT_PROMPTS = {
    "field_extraction_v1": """Extract the value for the following field from the document context.

Field name: {field_name}
Field type: {field_type}
Known aliases: {aliases}
Description: {description}

Document context:
{context}

Instructions:
1. Extract ONLY from the provided context
2. If not found, return NOT FOUND
3. Return the raw value without units unless part of the value
4. Match the expected format for {field_type}

<FINAL_VALUE>extracted value or NOT FOUND</FINAL_VALUE>""",

    "field_extraction_v2": """You are a precise financial document extraction system.

FIELD: {field_name}
TYPE:  {field_type}
ALIASES: {aliases}
DESCRIPTION: {description}

DOCUMENT CONTEXT:
{context}

EXTRACTION RULES:
- Extract ONLY what is explicitly stated in the context above
- Do NOT infer, estimate, or calculate values
- If field_name or any alias appears multiple times, extract the PRIMARY value
- Format: match the {field_type} format exactly
- If not found in context: return NOT FOUND (not null, not empty, not 0)

Think step by step:
1. Look for {field_name} and aliases: {aliases}
2. Find the associated value
3. Verify it matches {field_type} format

<FINAL_VALUE>extracted value or NOT FOUND</FINAL_VALUE>""",
}

class PromptRegistryService:
    """
    Prompt versioning — every prompt stored with a content hash.
    Every extraction linked to exact prompt_id used.

    Why this matters: if accuracy drops after a prompt change,
    you can trace exactly which prompt caused it and roll back.
    """

    def __init__(self, db: Session):
        self.db = db
        self._seed_defaults()

    def _seed_defaults(self):
        """Ensure default prompts exist on startup."""
        for name, template in DEFAULT_PROMPTS.items():
            self.register(name, template)

    def register(self, prompt_name: str, template_text: str) -> str:
        """Register prompt with content hash. Returns prompt_id."""
        content_hash = hashlib.sha256(template_text.encode()).hexdigest()[:8]
        prompt_id    = f"{prompt_name}:{content_hash}"

        existing = self.db.query(PromptVersion).filter_by(prompt_id=prompt_id).first()
        if not existing:
            record = PromptVersion(
                prompt_id     = prompt_id,
                prompt_name   = prompt_name.rsplit("_v", 1)[0] if "_v" in prompt_name else prompt_name,
                version_tag       = "v" + prompt_name.rsplit("_v", 1)[-1] if "_v" in prompt_name else "v1",
                body = template_text,
                is_active     = True,
            )
            self.db.add(record)
            self.db.commit()
        return prompt_id

    def get_active(self, prompt_name: str = "field_extraction") -> tuple[str, str]:
        """Returns (prompt_id, template_text) for the active prompt."""
        record = (
            self.db.query(PromptVersion)
            .filter_by(prompt_name=prompt_name, is_active=True)
            .order_by(PromptVersion.created_at.desc())
            .first()
        )
        if not record:
            # Fallback to v2 default
            template = DEFAULT_PROMPTS["field_extraction_v2"]
            prompt_id = self.register("field_extraction_v2", template)
            return prompt_id, template
        return record.prompt_id, record.body

    def format_prompt(self, template: str, field, context: str) -> str:
        """Fill prompt template with field definition and retrieved context."""
        return template.format(
            field_name  = field.name,
            field_type  = field.field_type.value,
            aliases     = ", ".join(field.aliases) if field.aliases else "none",
            description = field.description or "not specified",
            context     = context,
        )
```

---

### `backend\services\reextractor.py`

```python
# backend/services/reextractor.py
import re
import os
from groq import Groq
from anthropic import Anthropic
from services.vector_store import VectorStoreService
from services.schema_service import FieldDefinition

class ReExtractor:
    """
    Implements Re-extraction path from the Confidence Gate.
    The biggest gap from the original 10 projects.

    3 escalating strategies:
    1. expand_context: top-k 3 → 6 (more context might contain the answer)
    2. hyde_query: HyDE — generate hypothetical answer, use as query
       (finds chunks by semantic similarity to expected format)
    3. different_model: switch LLM provider as last resort

    After 3 failed attempts → EXTRACTION_FAILED → escalate to analyst
    with full retry trace attached.
    """

    MAX_ATTEMPTS = 3

    def __init__(self, db):
        # self.client = Anthropic()
        self.client = Groq(api_key=os.environ.get("GROQ_API"))
        self.vs     = VectorStoreService()

    def retry(self, field: FieldDefinition, doc_hash: str,
              schema_service, attempt: int = 1) -> dict:
        """Recursive retry with escalating strategies."""

        if attempt > self.MAX_ATTEMPTS:
            return {
                "status":   "EXTRACTION_FAILED",
                "attempts": attempt - 1,
                "escalate": True,
                "message":  "All retry strategies exhausted. Routing to analyst with trace."
            }

        strategy = {1: "expand_context", 2: "hyde_query", 3: "different_model"}[attempt]

        # Get chunks based on strategy
        if strategy == "expand_context":
            query  = schema_service.build_query(field)
            chunks = self.vs.retrieve(query, doc_hash, top_k=6)  # 3→6

        elif strategy == "hyde_query":
            hypo_query = self._generate_hypothetical(field)
            chunks     = self.vs.retrieve(hypo_query, doc_hash, top_k=4)

        else:  # different_model — use same chunks, different model
            query  = schema_service.build_query(field)
            chunks = self.vs.retrieve(query, doc_hash, top_k=3)

        if not chunks:
            return self.retry(field, doc_hash, schema_service, attempt + 1)

        context  = "\n\n---\n\n".join(c["text"] for c in chunks)
        value    = self._extract(field, context, strategy, attempt)

        result = {
            "status":   "retry_success" if value not in ["EXTRACTION_FAILED", "NOT FOUND", ""] else "retry_failed",
            "attempt":  attempt,
            "strategy": strategy,
            "value":    value,
            "chunk_ids":[c["point_id"] for c in chunks],
        }

        if result["status"] == "retry_failed":
            result["next"] = self.retry(field, doc_hash, schema_service, attempt + 1)

        return result

    def _generate_hypothetical(self, field: FieldDefinition) -> str:
        """
        HyDE: Hypothetical Document Embeddings.
        Generate how this field WOULD appear in a real document.
        The hypothetical answer is semantically closer to real document
        text than the bare field name is.

        Example:
        field = "Total Revenue"
        hypothetical = "Total Revenue: $4.5 million for the fiscal year 2024"
        This query retrieves chunks matching the FORMAT, not just the label.
        """
        prompt= f"""
        Write one example sentence showing how '{field.name}' (type: {field.field_type}) would appear in a financial document. Be brief and realistic.

        Example format: "Total Revenue: $4.5M" or "Revenue for fiscal year was $4,500,000"
        Reply with only the example sentence.
            """
#         response = self.client.messages.create(
#             model="claude-sonnet-4-20250514",
#             max_tokens=80,
#             messages=[{"role": "user", "content": f"""Write one example sentence showing how '{field.name}' (type: {field.field_type}) would appear in a financial document. Be brief and realistic.
#
# Example format: "Total Revenue: $4.5M" or "Revenue for fiscal year was $4,500,000"
# Reply with only the example sentence."""}]
#         )
#         return response.content[0].text.strip()
        answer = self.client.chat.completions.create(
            messages=[
                {
                    "role": "user",
                    "content": prompt
                }
            ],
            model="llama-3.3-70b-versatile",
            max_tokens=80)
        raw_output = answer.choices[0].message.content.strip()
        return raw_output

    def _extract(self, field: FieldDefinition, context: str,
                 strategy: str, attempt: int) -> str:
        prompt = f"""[Retry attempt {attempt}, Strategy: {strategy}]
    Extract the value for: {field.name} (type: {field.field_type})
    Known synonyms: {', '.join(field.aliases) or 'none'}

    Context:
    {context}

    If found: <FINAL_VALUE>value</FINAL_VALUE>
    If truly not present: <FINAL_VALUE>NOT FOUND</FINAL_VALUE>"""

        response = self.client.chat.completions.create(  # ← Groq syntax
            messages=[{"role": "user", "content": prompt}],
            model="llama-3.3-70b-versatile",
            max_tokens=150,
        )
        raw = response.choices[0].message.content  # ← Groq response format
        match = re.search(r"<FINAL_VALUE>(.*?)</FINAL_VALUE>", raw, re.DOTALL)
        return match.group(1).strip() if match else "EXTRACTION_FAILED"
```

---

### `backend\services\schema_service.py`

```python
# backend/services/schema_service.py
import hashlib, json, re
from enum import Enum
from typing import List, Optional
from pydantic import BaseModel, field_validator
from sqlalchemy.orm import Session
from models.db import SchemaRegistry

class FieldType(str, Enum):
    CURRENCY   = "currency"
    DATE       = "date"
    PERCENTAGE = "percentage"
    EIN        = "ein"
    TEXT       = "text"
    NUMBER     = "number"
    BOOLEAN    = "boolean"

# Type-specific regex validators
FIELD_VALIDATORS = {
    FieldType.CURRENCY:   r"^\$?[\d,]+(\.\d{2})?([MBKmb])?$|^[\d,]+(\.\d+)?$",
    FieldType.PERCENTAGE: r"^\d{1,3}(\.\d+)?%?$",
    FieldType.EIN:        r"^\d{2}-\d{7}$",
    FieldType.DATE:       r"\d{4}[-/]\d{2}[-/]\d{2}|\d{2}[-/]\d{2}[-/]\d{4}",
    FieldType.NUMBER:     r"^[\d,]+(\.\d+)?$",
}

class FieldDefinition(BaseModel):
    """
    Single field in a user-defined schema.

    aliases: The fix for "schema failure" from the Codebasics failure modes.
    When field name in document ("Net Sales") doesn't match schema name ("Revenue"),
    extraction returns "Not Found". Aliases = synonyms for the same concept.

    region: Only present for mode="ocr" schemas (see hitl-go/routers/documents.go's
    handleUploadOCR) — the PDF bounding box {page, x, y, w, h} drawn in
    PDFRegionEditor. None for LLM/RAG-mode schemas, which locate fields by
    semantic search instead of a fixed region.
    """
    name:        str
    field_type:  FieldType
    aliases:     List[str] = []   # e.g. ["Net Sales", "Total Revenue", "Gross Revenue"]
    required:    bool = True
    description: Optional[str] = None
    region:      Optional[dict] = None

    @field_validator("name")
    @classmethod
    def name_must_not_be_empty(cls, v):
        if not v.strip():
            raise ValueError("Field name cannot be empty")
        return v.strip()


class SchemaDefinition(BaseModel):
    schema_name: str
    version:     str
    fields:      List[FieldDefinition]
    # "llm" (default) = RAG/semantic-search extraction via services/extractor.py.
    # "ocr" = direct region-crop text extraction (services/ocr_extractor.py),
    # no embeddings, no vector search, no LLM call of any kind. Set by
    # hitl-go/routers/documents.go's handleUploadOCR; the LLM upload path
    # (handleUpload) never sets this, so it defaults to "llm" for schemas
    # that predate this field too — existing behavior is unchanged for them.
    mode: str = "llm"


class SchemaServiceImpl:
    def __init__(self, db: Session):
        self.db = db

    def register(self, doc_schema: SchemaDefinition) -> str:
        """
        Hash schema for immutable versioning.
        Same schema content = same hash = no duplicate registration.
        """
        schema_json = doc_schema.model_dump_json(indent=None)
        schema_hash = hashlib.sha256(schema_json.encode()).hexdigest()[:12]
        schema_id   = f"{doc_schema.schema_name}:{doc_schema.version}:{schema_hash}"

        existing = self.db.query(SchemaRegistry).filter_by(schema_id=schema_id).first()
        if not existing:
            record = SchemaRegistry(
                schema_id       = schema_id,
                schema_name     = doc_schema.schema_name,
                version         = doc_schema.version,
                definition_json = schema_json,
            )
            self.db.add(record)
            self.db.commit()
        return schema_id

    def get(self, schema_id: str) -> SchemaDefinition:
        row = self.db.query(SchemaRegistry).filter_by(schema_id=schema_id).first()
        if not row:
            raise ValueError(f"Schema {schema_id} not found")
        return SchemaDefinition.model_validate_json(row.definition_json)

    def list_all(self) -> List[dict]:
        rows = self.db.query(SchemaRegistry).order_by(SchemaRegistry.created_at.desc()).all()
        return [{"schema_id": r.schema_id, "name": r.schema_name, "version": r.version,
                 "created_at": str(r.created_at)} for r in rows]

    def validate_value(self, value: str, field: FieldDefinition) -> tuple[bool, str]:
        """
        Type-aware validation. Applied AFTER LLM extraction, BEFORE confidence scoring.
        Returns (is_valid, reason).
        """
        if not value or value.strip() in ["", "NOT FOUND", "N/A", "EXTRACTION_FAILED"]:
            if field.required:
                return False, "required_field_empty"
            return True, "optional_field_empty"

        pattern = FIELD_VALIDATORS.get(field.field_type)
        if pattern and not re.search(pattern, value.strip(), re.IGNORECASE):
            return False, f"value_does_not_match_{field.field_type}_format"

        return True, "valid"

    def build_query(self, field: FieldDefinition) -> str:
        """
        Build retrieval query incorporating field name + all aliases.
        This ensures vector search finds chunks that use any synonym.
        e.g. "Revenue Net Sales Total Revenue Gross Revenue"
        """
        parts = [field.name] + field.aliases
        if field.description:
            parts.append(field.description)
        return " ".join(parts)
```

---

### `backend\services\table_tagger.py`

```python
# backend/services/table_tagger.py
import json
from enum import Enum
from typing import List
from anthropic import Anthropic
from groq import Groq
import os
class TableType(str, Enum):
    FINANCIAL_SUMMARY = "financial_summary"  # P&L, balance sheet totals
    LINE_ITEMS        = "line_items"         # invoice rows, product list
    FOOTNOTES         = "footnotes"          # reference/note tables
    FORM_FIELDS       = "form_fields"        # key-value form data
    COMPARISON        = "comparison"         # side-by-side column data

# From Codebasics "Join Strategies" slide:
# JSON-per-row = highest LLM comprehension, lowest hallucination risk
SERIALIZATION_STRATEGY = {
    TableType.FINANCIAL_SUMMARY: "json_per_row",    # {"Revenue": "$4.5M", "EBITDA": "$1.2M"}
    TableType.LINE_ITEMS:        "markdown_table",  # | Item | Qty | Price |
    TableType.FOOTNOTES:         "prose",            # plain text join
    TableType.FORM_FIELDS:       "key_value",        # Revenue: $4.5M
    TableType.COMPARISON:        "header_aware",     # Revenue: Q1=$2M | Q2=$2.5M
}

class TableTagger:
    def __init__(self):
        # self.client = Anthropic()

        self.client = Groq(api_key=os.environ.get("GROQ_API"))
    def classify(self, table_rows: list, surrounding_text: str) -> TableType:
        """
        One LLM call to classify table type.
        Sample = first 3 rows to keep prompt short.
        """
        if not table_rows:
            return TableType.FORM_FIELDS

        sample_text = str(table_rows[:3])
        prompt = f"""Classify this table into EXACTLY ONE category from this list:
financial_summary | line_items | footnotes | form_fields | comparison

Table sample (first 3 rows): {sample_text[:400]}
Surrounding document text: {surrounding_text[:200]}

Rules:
- financial_summary: totals, aggregates, P&L, balance sheet
- line_items: repeated rows with same structure (invoice lines, products)
- footnotes: numbered references, small auxiliary tables
- form_fields: key-value pairs like a form
- comparison: side-by-side columns comparing periods/entities

Reply with ONLY the category name, nothing else."""

        # response = self.client.messages.create(
        #     model="claude-sonnet-4-20250514",
        #     max_tokens=20,
        #     messages=[{"role": "user", "content": prompt}]
        # )
        # tag = response.content[0].text.strip().lower()

        answer = self.client.chat.completions.create(
            messages=[
                {
                    "role": "user",
                    "content": prompt
                }
            ],
            model="llama-3.3-70b-versatile",
            max_tokens=20)
        tag = answer.choices[0].message.content.strip().lower()
        # Safe fallback if LLM returns unexpected value
        return TableType(tag) if tag in TableType._value2member_map_ else TableType.FINANCIAL_SUMMARY

    def serialize(self, table_rows: List[list], headers: List[str], table_type: TableType) -> str:
        """
        Apply type-specific serialization strategy.

        Key insight (from Codebasics Join Strategies):
        - Raw row join: "ACME Corp, $4.5M" → LOW LLM comprehension (no headers)
        - JSON per row: {"Company":"ACME","Revenue":"$4.5M"} → HIGHEST comprehension
        - Header-aware: "Revenue: $4.5M | EBITDA: $1.2M" → HIGH comprehension
        """
        if not table_rows or not headers:
            return ""

        strategy = SERIALIZATION_STRATEGY[table_type]

        if strategy == "json_per_row":
            # Best for financial tables — LLM always knows which column = which field
            rows_json = []
            for row in table_rows:
                if row and any(cell for cell in row if cell):  # skip empty rows
                    row_dict = {h: (str(v) if v is not None else "") for h, v in zip(headers, row)}
                    rows_json.append(json.dumps(row_dict))
            return "\n".join(rows_json)

        elif strategy == "markdown_table":
            # Good for line items — preserves visual structure
            header_row = "| " + " | ".join(str(h) for h in headers) + " |"
            separator  = "| " + " | ".join("---" for _ in headers) + " |"
            data_rows  = "\n".join(
                "| " + " | ".join(str(c) if c else "" for c in row) + " |"
                for row in table_rows if row
            )
            return f"{header_row}\n{separator}\n{data_rows}"

        elif strategy == "key_value":
            # For form fields — "Revenue: $4.5M"
            lines = []
            for row in table_rows:
                for h, v in zip(headers, row):
                    if v:
                        lines.append(f"{h}: {v}")
            return "\n".join(lines)

        elif strategy == "header_aware":
            # "Revenue: Q1=$2M | Q2=$2.5M"
            return "\n".join(
                " | ".join(f"{h}: {v}" for h, v in zip(headers, row) if v)
                for row in table_rows if row
            )

        else:  # prose — footnotes
            return " ".join(
                str(cell) for row in table_rows for cell in row if cell
            )

    def get_column_headers(self, table_rows: list) -> tuple[list, list]:
        """
        Heuristic: first row is headers if it contains no numeric values.
        Returns (headers, data_rows).
        """
        if not table_rows:
            return [], []
        first_row = table_rows[0] or []
        # Check if first row looks like headers (mostly strings, no numbers)
        is_header = all(
            not str(cell).replace(",","").replace(".","").replace("$","").replace("%","").strip().isdigit()
            for cell in first_row if cell
        )
        if is_header:
            headers = [str(h).strip() if h else f"Col{i}" for i, h in enumerate(first_row)]
            return headers, table_rows[1:]
        else:
            headers = [f"Column{i+1}" for i in range(len(first_row))]
            return headers, table_rows
```

---

### `backend\services\vector_store.py`

```python
# backend/services/vector_store.py
import uuid
from typing import List, Dict, Any
from qdrant_client import QdrantClient
from qdrant_client.models import (
    Distance, VectorParams, PointStruct,
    Filter, FieldCondition, MatchValue
)
from sentence_transformers import SentenceTransformer

class VectorStoreService:
    """
    Implements Vector Indexing & Retrieval Layer from mind map:
    - Embedding generation (SentenceTransformer)
    - Namespace isolation (per-document Qdrant collections)
    - Persistent vector store (Qdrant, not in-memory FAISS)
    - Top-k retrieval with metadata filters
    - Diagnostics logging (point_id returned with every result)

    Critical design: each document = its own Qdrant collection.
    This is namespace isolation — tenant A's vectors never pollute tenant B.
    Also enables fast filtered search (only this document's chunks).
    """

    EMBEDDING_MODEL = "all-MiniLM-L6-v2"   # 384-dim, fast, good for financial text
    VECTOR_DIM      = 384

    def __init__(self, host: str = "localhost", port: int = 6333):
        self.client = QdrantClient(host=host, port=port)
        self.model  = SentenceTransformer(self.EMBEDDING_MODEL)

    def _collection_name(self, doc_hash: str) -> str:
        """Namespace isolation: each document gets a unique collection."""
        return f"doc_{doc_hash[:20]}"

    def ensure_collection(self, doc_hash: str):
        """Create collection if it doesn't exist (idempotent)."""
        name = self._collection_name(doc_hash)
        existing = {c.name for c in self.client.get_collections().collections}
        if name not in existing:
            self.client.create_collection(
                collection_name=name,
                vectors_config=VectorParams(size=self.VECTOR_DIM, distance=Distance.COSINE)
            )
        return name

    def index_chunks(self, chunks: List[Dict[str, Any]], doc_hash: str) -> int:
        """
        Embed all chunks and store in Qdrant with full metadata payload.

        Key: payload stored WITH vector → retrieval returns text + metadata
        in one query. No separate DB lookup needed.
        """
        if not chunks:
            return 0

        collection = self.ensure_collection(doc_hash)
        texts   = [c["text"] for c in chunks]
        vectors = self.model.encode(texts, batch_size=32, show_progress_bar=False).tolist()

        points = [
            PointStruct(
                id      = str(uuid.uuid4()),
                vector  = vec,
                payload = {
                    "text":       chunk["text"],
                    "page_num":   chunk["page_num"],
                    "section":    chunk["section"],
                    "chunk_type": chunk["chunk_type"],
                    "doc_hash":   chunk["doc_hash"],
                    "chunk_id":   chunk["chunk_id"],
                    "table_type": chunk.get("table_type"),
                    "headers":    chunk.get("headers", []),
                }
            )
            for vec, chunk in zip(vectors, chunks)
        ]

        # Upsert in batches of 100
        batch_size = 100
        for i in range(0, len(points), batch_size):
            self.client.upsert(collection_name=collection, points=points[i:i+batch_size])

        return len(points)

    def retrieve(self, query: str, doc_hash: str, top_k: int = 3) -> List[Dict[str, Any]]:
        """
        Top-k similarity search within document namespace.
        Returns chunks WITH scores and point_ids for diagnostics logging.

        Diagnostics: every retrieved chunk's point_id is stored in the
        Correction record → you can trace exactly which chunks were used
        for any extraction, ever.
        """
        collection = self._collection_name(doc_hash)
        query_vec  = self.model.encode([query])[0].tolist()

        try:
            results = self.client.search(
                collection_name=collection,
                query_vector=query_vec,
                limit=top_k,
                with_payload=True,
                with_vectors=False,
            )
        except Exception:
            return []

        return [
            {
                **r.payload,
                "retrieval_score": round(r.score, 4),
                "point_id":        str(r.id),   # for diagnostics logging
            }
            for r in results
        ]

    def delete_document(self, doc_hash: str):
        """Clean up document namespace (GDPR compliance)."""
        try:
            self.client.delete_collection(self._collection_name(doc_hash))
        except Exception:
            pass
```

---

### `backend\utils\__init__.py`

```python
# backend/utils/__init__.py

```

---

### `backend\utils\db_backup.py`

```python
# backend/utils/db_backup.py
"""
Called during graceful shutdown.
Snapshots ALL DB tables → export/db_backup_<timestamp>.json
so state can be inspected or restored after a restart.
"""
import json
import logging
import os
from datetime import datetime

from models.db import (
    SessionLocal,
    User, Document, SchemaRegistry,
    ExtractionJob, Extraction, Correction,
    AuditLog, Chunk, PromptVersion,
)

logger    = logging.getLogger(__name__)
EXPORT_DIR = os.getenv("EXPORT_DIR", "export")


def _safe_str(val) -> str | None:
    return str(val) if val is not None else None


def backup_db_state() -> str | None:
    """
    Write a full DB snapshot to disk.
    Returns the file path written, or None if backup failed.
    """
    os.makedirs(EXPORT_DIR, exist_ok=True)
    db = SessionLocal()
    try:
        # ── Query all tables ──────────────────────────────────────────────────
        users         = db.query(User).all()
        documents     = db.query(Document).all()
        schemas       = db.query(SchemaRegistry).all()
        jobs          = db.query(ExtractionJob).all()
        extractions   = db.query(Extraction).all()
        corrections   = db.query(Correction).all()
        audit_logs    = db.query(AuditLog).order_by(AuditLog.id.desc()).limit(1000).all()
        chunks        = db.query(Chunk).all()
        prompts       = db.query(PromptVersion).all()

        snapshot = {
            "backed_up_at": datetime.utcnow().isoformat(),
            "counts": {
                "users":       len(users),
                "documents":   len(documents),
                "schemas":     len(schemas),
                "jobs":        len(jobs),
                "extractions": len(extractions),
                "corrections": len(corrections),
                "audit_logs":  len(audit_logs),
                "chunks":      len(chunks),
                "prompts":     len(prompts),
            },

            # ── Users ─────────────────────────────────────────────────────────
            "users": [
                {
                    "id":         u.id,
                    "username":   u.username,
                    "email":      u.email,
                    "role":       u.role,
                    "is_active":  u.is_active,
                    "created_at": _safe_str(u.created_at),
                    "last_login": _safe_str(u.last_login),
                }
                for u in users
            ],

            # ── Documents ─────────────────────────────────────────────────────
            "documents": [
                {
                    "doc_id":       d.doc_id,
                    "doc_hash":     d.doc_hash,
                    "filename":     d.filename,
                    "status":       d.status,
                    "doc_type":     d.doc_type,
                    "page_count":   d.page_count,
                    "user_id":      d.user_id,
                    "created_at":   _safe_str(d.created_at),
                    "updated_at":   _safe_str(d.updated_at),
                }
                for d in documents
            ],

            # ── Schema Registry ───────────────────────────────────────────────
            "schemas": [
                {
                    "schema_id":   s.schema_id,
                    "schema_name": s.schema_name,
                    "version":     s.version,
                    "created_at":  _safe_str(s.created_at),
                    "created_by":  s.created_by,
                }
                for s in schemas
            ],

            # ── Extraction Jobs ───────────────────────────────────────────────
            "extraction_jobs": [
                {
                    "extraction_id": j.extraction_id,
                    "status":        j.status,
                    "schema_id":     j.schema_id,
                    "pdf_path":      j.pdf_path,
                    "doc_hash":      j.doc_hash,
                    "error":         j.error,
                    "created_at":    _safe_str(j.created_at),
                    "started_at":    _safe_str(j.started_at),
                    "completed_at":  _safe_str(j.completed_at),
                    "sla_deadline":  _safe_str(j.sla_deadline),
                }
                for j in jobs
            ],

            # ── Extractions ───────────────────────────────────────────────────
            "extractions": [
                {
                    "extraction_id":      e.extraction_id,
                    "doc_id":             e.doc_id,
                    "schema_id":          e.schema_id,
                    "status":             e.status,
                    "auto_accepted":      e.auto_accepted,
                    "queued_for_review":  e.queued_for_review,
                    "failed_count":       e.failed_count,
                    "overall_confidence": e.overall_confidence,
                    "created_at":         _safe_str(e.created_at),
                    "completed_at":       _safe_str(e.completed_at),
                }
                for e in extractions
            ],

            # ── Corrections ───────────────────────────────────────────────────
            "corrections": [
                {
                    "id":               c.id,
                    "extraction_id":    c.extraction_id,
                    "field_name":       c.field_name,
                    "original_value":   c.original_value,
                    "corrected_value":  c.corrected_value,
                    "failure_type":     c.failure_type,
                    "analyst_id":       c.analyst_id,
                    "schema_id":        c.schema_id,
                    "prompt_id":        c.prompt_id,
                    "confidence_score": c.confidence_score,
                    "corrected_at":     _safe_str(c.corrected_at),
                }
                for c in corrections
            ],

            # ── Audit Log (last 1000 entries only) ────────────────────────────
            "audit_log": [
                {
                    "id":          a.id,
                    "path":        a.path,
                    "method":      a.method,
                    "user_id":     a.user_id,
                    "status_code": a.status_code,
                    "duration_ms": a.duration_ms,
                    "timestamp":   _safe_str(a.timestamp),
                }
                for a in audit_logs
            ],

            # ── Prompt Versions ───────────────────────────────────────────────
            "prompts": [
                {
                    "prompt_id":   p.prompt_id,
                    "prompt_name": p.prompt_name,
                    "version_tag": p.version_tag,
                    "is_active":   p.is_active,
                    "created_at":  _safe_str(p.created_at),
                    "created_by":  p.created_by,
                }
                for p in prompts
            ],
        }

        ts    = datetime.utcnow().strftime("%Y%m%d_%H%M%S")
        fpath = os.path.join(EXPORT_DIR, f"db_backup_{ts}.json")

        with open(fpath, "w") as f:
            json.dump(snapshot, f, indent=2, default=str)

        counts = snapshot["counts"]
        logger.info(
            "DB backup written → %s  "
            "(%d jobs, %d corrections, %d users, %d docs, %d prompts)",
            fpath,
            counts["jobs"], counts["corrections"],
            counts["users"], counts["documents"], counts["prompts"],
        )
        return fpath

    except Exception as e:
        logger.error("DB backup failed: %s", e)
        return None
    finally:
        db.close()

```

---

### `backend\utils\db_pool.py`

```python
# backend/utils/db_pool.py
"""
Cleanly disposes the SQLAlchemy engine connection pool on shutdown.
Prevents Postgres lock leaks and "too many connections" errors on restart.
"""
import logging
from models.db import engine

logger = logging.getLogger(__name__)


def drain_db_connections():
    """
    Dispose all connections in the pool.
    Any in-use connections finish their current transaction first,
    then are closed when returned to the pool.
    """
    try:
        engine.dispose()
        logger.info("SQLAlchemy connection pool disposed cleanly")
    except Exception as e:
        logger.error(f"Error disposing connection pool: {e}")

```

---

### `backend\utils\timing.py`

```python
# backend/utils/timing.py
#
# Lightweight timing context manager used throughout the extraction
# pipeline (services/extractor.py, services/evaluator.py) to log how long
# each stage takes — retrieval, prompt building, the LLM call itself,
# parsing/validation. Deliberately just logs; it doesn't write to a table,
# since these fire many times per document and a DB write per stage would
# add real latency to the extraction path for a diagnostic that's mainly
# useful when actively debugging performance, not for routine operation.
#
# Usage:
#   with timed("extraction", "llm_call", field_name="Total Revenue", meta={"model": "..."}):
#       ...
import logging
import time
from contextlib import contextmanager

logger = logging.getLogger("timing")


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

```

---

### `backend\workers\__init__.py`

```python
# backend/workers/__init__.py

```

---

### `backend\workers\celery_app.py`

```python
import os
import json
import logging
import shutil
import subprocess
from datetime import datetime, timedelta

from celery import Celery
from celery.signals import worker_init, worker_shutdown, task_prerun, task_postrun
from sqlalchemy import text

from models.db import init_db, SessionLocal, ExtractionJob

logger     = logging.getLogger(__name__)
REDIS_HOST = os.getenv("REDIS_HOST", "localhost")
EXPORT_DIR = os.getenv("EXPORT_DIR", "export")
SLA_HOURS  = int(os.getenv("SLA_HOURS", "24"))

celery_app = Celery(
    "hitl_platform",
    broker  = f"redis://{REDIS_HOST}:6379/1",
    backend = f"redis://{REDIS_HOST}:6379/2",
)

celery_app.conf.update(
    task_acks_late             = True,
    task_reject_on_worker_lost = True,
    worker_prefetch_multiplier = 1,
    task_serializer   = "json",
    accept_content    = ["json"],
    result_serializer = "json",
    timezone   = "UTC",
    enable_utc = True,
    broker_connection_retry_on_startup = True,
    worker_cancel_long_running_tasks_on_connection_loss = True,
)

# ── Worker lifecycle signals ──────────────────────────────────────────────────

@worker_init.connect
def on_worker_start(**kwargs):
    logger.info("Celery worker started — initialising DB...")
    init_db()
    logger.info("Celery worker ready")


@worker_shutdown.connect
def on_worker_shutdown(**kwargs):
    logger.info("Celery worker shutting down — writing checkpoint...")
    _write_checkpoint()
    logger.info("Checkpoint written. Worker exiting cleanly.")


@task_prerun.connect
def on_task_start(task_id, task, args, kwargs, **extras):
    extraction_id = args[0] if args else None
    if not extraction_id:
        return
    db = SessionLocal()
    try:
        job = db.query(ExtractionJob).filter_by(extraction_id=extraction_id).first()
        if job:
            job.started_at = datetime.utcnow()
            db.commit()
    except Exception as e:
        logger.error("task_prerun DB update failed: %s", e)
    finally:
        db.close()
    logger.info("Task started  | task_id=%s | extraction_id=%s", task_id, extraction_id)


@task_postrun.connect
def on_task_end(task_id, task, args, kwargs, retval, state, **extras):
    logger.info("Task finished | task_id=%s | state=%s", task_id, state)


# ── Checkpoint ────────────────────────────────────────────────────────────────

def _write_checkpoint():
    os.makedirs(EXPORT_DIR, exist_ok=True)
    db = SessionLocal()
    try:
        incomplete = db.query(ExtractionJob).filter(
            ExtractionJob.status.in_(["processing", "pending"])
        ).all()
        checkpoint = {
            "timestamp": datetime.utcnow().isoformat(),
            "incomplete_jobs": [
                {
                    "extraction_id": j.extraction_id,
                    "status":        j.status,
                    "schema_id":     j.schema_id,
                    "pdf_path":      j.pdf_path,
                    "doc_hash":      j.doc_hash,
                    "sla_deadline":  str(j.sla_deadline) if j.sla_deadline else None,
                }
                for j in incomplete
            ],
        }
        fpath = os.path.join(EXPORT_DIR, "checkpoint.json")
        with open(fpath, "w") as f:
            json.dump(checkpoint, f, indent=2)
        logger.info("Checkpoint saved → %s  (%d incomplete jobs)", fpath, len(incomplete))
    except Exception as e:
        logger.error("Checkpoint write failed: %s", e)
    finally:
        db.close()


# ── Retry helper ──────────────────────────────────────────────────────────────

def get_retry_value(retry_result: dict) -> str | None:
    if not retry_result:
        return None
    if retry_result.get("status") == "retry_success":
        return retry_result.get("value")
    if "next" in retry_result:
        return get_retry_value(retry_result["next"])
    return None


def gate_enqueue_ocr_field(extraction_id: str, field, extraction: dict) -> dict:
    from hitl.sla_queue import SLAQueue
    queue = SLAQueue()
    item = {
        "item_id":       f"{extraction_id}:{field.name}",
        "extraction_id": extraction_id,
        "field_name":    field.name,
        "priority":      50,
        "result":        extraction,
    }
    queue.push(item, sla_hours=1)
    return item


# ── Format conversion helpers ─────────────────────────────────────────────────

_IMAGE_EXTS = {".png", ".jpg", ".jpeg", ".tiff", ".tif", ".bmp", ".gif", ".webp"}
_WORD_EXTS  = {".docx", ".doc"}


def _get_original_filename(db, extraction_id: str) -> tuple[str, str]:
    row = db.execute(
        text("SELECT filename FROM documents WHERE extraction_id = :eid"),
        {"eid": extraction_id},
    ).fetchone()
    if row and row[0]:
        fn = row[0]
        return fn, os.path.splitext(fn)[1].lower()
    return "", ""


def _ensure_pdf(input_path: str, export_dir: str) -> tuple[str, str]:
    """
    Convert input file to PDF if it is not already one.
    Returns (path_to_pdf, original_extension).
    """
    ext = os.path.splitext(input_path)[1].lower()
    if ext == ".pdf":
        return input_path, ext

    converted_dir = os.path.join(export_dir, "converted")
    os.makedirs(converted_dir, exist_ok=True)

    base_name = os.path.splitext(os.path.basename(input_path))[0]
    pdf_path = os.path.join(converted_dir, f"{base_name}.pdf")

    if os.path.exists(pdf_path):
        return pdf_path, ext

    if ext in _WORD_EXTS:
        soffice = shutil.which("soffice") or shutil.which("libreoffice")
        if not soffice:
            raise RuntimeError(
                "LibreOffice (soffice) not found in PATH — cannot convert Word documents."
            )
        cmd = [
            soffice, "--headless", "--convert-to", "pdf",
            "--outdir", converted_dir, input_path
        ]
        try:
            subprocess.run(cmd, check=True, capture_output=True, timeout=120)
        except subprocess.CalledProcessError as exc:
            logger.error("LibreOffice conversion failed: %s", exc.stderr.decode())
            raise RuntimeError(f"LibreOffice failed to convert {input_path}")
        lo_out = os.path.join(converted_dir, f"{base_name}.pdf")
        if os.path.exists(lo_out) and lo_out != pdf_path:
            os.rename(lo_out, pdf_path)

    elif ext in _IMAGE_EXTS:
        import fitz
        img_doc = fitz.open(input_path)
        pdfbytes = img_doc.convert_to_pdf()
        pdf_doc = fitz.open("pdf", pdfbytes)
        pdf_doc.save(pdf_path)
        pdf_doc.close()
        img_doc.close()
    else:
        raise ValueError(f"Unsupported file format for OCR pipeline: {ext}")

    return pdf_path, ext


def _pdf_to_image(pdf_path: str, output_path: str, dpi: int = 200):
    """Render first page of a PDF to a PNG image."""
    import fitz
    doc = fitz.open(pdf_path)
    if doc.page_count == 0:
        doc.close()
        raise ValueError("PDF has no pages")
    page = doc[0]
    pix = page.get_pixmap(dpi=dpi)
    pix.save(output_path)
    doc.close()


# ── Main extraction task ──────────────────────────────────────────────────────

@celery_app.task(
    bind        = True,
    max_retries = 3,
    acks_late   = True,
)
def process_document(
    self,
    extraction_id: str,
    pdf_path:      str,
    schema_id:     str,
    doc_hash:      str,
):
    """
    Full async extraction pipeline. Supports PDF, Word and image inputs.
    Non-PDF files are transparently converted to PDF before parsing.
    """
    init_db()
    db  = SessionLocal()
    job = None

    try:
        job = db.query(ExtractionJob).filter_by(extraction_id=extraction_id).first()
        if not job:
            logger.error("Job not found in DB: %s", extraction_id)
            return

        original_filename, orig_ext = _get_original_filename(db, extraction_id)

        job.status     = "processing"
        job.started_at = datetime.utcnow()
        job.error      = None
        db.commit()

        working_pdf_path, _ = _ensure_pdf(pdf_path, EXPORT_DIR)

        from services.schema_service import SchemaServiceImpl
        schema_svc = SchemaServiceImpl(db)
        schema     = schema_svc.get(schema_id)
        field_results = []

        if schema.mode == "ocr":
            from services.ocr_extractor import RegionOCRExtractor
            ocr = RegionOCRExtractor()
            for field in schema.fields:
                extraction = ocr.extract_field(field, working_pdf_path)
                decision = "human_review" if extraction["is_valid"] else "fail"
                queue_item = None
                if decision in ("human_review", "fail"):
                    queue_item = gate_enqueue_ocr_field(
                        extraction_id=extraction_id, field=field, extraction=extraction,
                    )
                field_results.append({
                    "field_name":        field.name,
                    "value":             extraction["raw_value"],
                    "confidence_score":  None,
                    "context_relevance": None,
                    "groundedness":      None,
                    "answer_relevance":  None,
                    "decision":          decision,
                    "item_id":           queue_item.get("item_id") if queue_item else None,
                    "prompt_id":         "none",
                    "retrieved_chunks":  [],
                    "extraction_method": extraction["guard_reason"],
                })
        else:
            from services.parser_service  import ParserService
            from services.normalizer      import Normalizer
            from services.vector_store    import VectorStoreService
            from services.extractor       import FieldExtractor
            from services.evaluator       import RAGEvaluator
            from services.confidence_gate import ConfidenceGate

            parser     = ParserService()
            normalizer = Normalizer()
            vs         = VectorStoreService()
            extractor  = FieldExtractor(db)
            evaluator  = RAGEvaluator()
            gate       = ConfidenceGate(db)

            parsed = parser.parse(working_pdf_path, doc_hash)
            chunks = normalizer.chunk_document(parsed)
            vs.index_chunks(chunks, doc_hash)

            for field in schema.fields:
                extraction = extractor.extract_field(
                    field=field, doc_hash=doc_hash,
                    schema_id=schema_id, schema_service=schema_svc,
                )
                eval_scores = evaluator.evaluate(
                    field_name      = field.name,
                    field_type      = field.field_type.value,
                    extracted_value = extraction["raw_value"],
                    retrieved_texts = extraction["retrieved_texts"],
                    query           = schema_svc.build_query(field),
                )
                gate_result = gate.decide(
                    extraction_id     = extraction_id,
                    extraction_result = extraction,
                    eval_scores       = eval_scores,
                    field             = field,
                    doc_hash          = doc_hash,
                    schema_service    = schema_svc,
                )
                if gate_result["decision"] == "re_extract":
                    retry_val   = get_retry_value(gate_result.get("retry", {}))
                    final_value = retry_val or extraction["raw_value"]
                else:
                    final_value = extraction["raw_value"]

                field_results.append({
                    "field_name":        field.name,
                    "value":             final_value,
                    "confidence_score":  eval_scores["confidence_score"],
                    "context_relevance": eval_scores["context_relevance"],
                    "groundedness":      eval_scores["groundedness"],
                    "answer_relevance":  eval_scores["answer_relevance"],
                    "decision":          gate_result["decision"],
                    "item_id":           gate_result.get("queue_item", {}).get("item_id"),
                    "prompt_id":         extraction["prompt_id"],
                    "retrieved_chunks":  extraction["retrieved_chunk_ids"],
                })

        needs_review = any(
            r["decision"] in ("human_review", "fail") for r in field_results
        )
        final_status = "pending" if needs_review else "complete"
        result_json_str = json.dumps(field_results)

        from workers.pending_cache import save_pending_result, delete_pending
        save_pending_result(extraction_id, final_status, result_json_str)

        job.status       = final_status
        job.result_json  = result_json_str
        job.completed_at = datetime.utcnow()
        job.error        = None
        db.commit()
        delete_pending(extraction_id)

        logger.info(
            "Extraction complete | id=%s | status=%s | fields=%d | orig=%s",
            extraction_id, job.status, len(field_results), original_filename or pdf_path,
        )

    except Exception as exc:
        logger.error(
            "Extraction failed | id=%s | attempt=%d/%d | err=%s",
            extraction_id, self.request.retries + 1, self.max_retries, exc,
        )
        if job:
            if self.request.retries >= self.max_retries:
                job.status = "failed"
                job.error  = str(exc)
                logger.error("All retries exhausted — marked as failed: %s", extraction_id)
            else:
                job.status = "processing"
            db.commit()
        raise self.retry(exc=exc, countdown=60)
    finally:
        db.close()


@celery_app.task(
    name="workers.celery_app.process_single_ocr_document",
    bind=True,
    max_retries=3,
    acks_late=True,
)
def process_single_ocr_document(
    self,
    extraction_id: str,
    pdf_path: str,
    schema_id: str,
    doc_hash: str,
    regions_json: str,
    serial_number: int = 1,
):
    """
    Direct single-PDF processing with pre-supplied corrections.
    Supports PDF, Word and image inputs. Image originals get a corrected PNG too.
    """
    import os
    import json
    from datetime import datetime

    init_db()
    db = SessionLocal()
    job = None

    try:
        job = db.query(ExtractionJob).filter_by(extraction_id=extraction_id).first()
        if not job:
            logger.error("Job not found in DB: %s", extraction_id)
            return

        original_filename, orig_ext = _get_original_filename(db, extraction_id)

        job.status = "processing"
        job.started_at = datetime.utcnow()
        job.error = None
        db.commit()

        working_pdf_path, _ = _ensure_pdf(pdf_path, EXPORT_DIR)

        from services.ocr_extractor import RegionOCRExtractor
        import fitz

        ocr = RegionOCRExtractor()
        regions = json.loads(regions_json)

        EXPORT_DIR = os.getenv("EXPORT_DIR", "export")
        CORRECTED_ROOT = os.path.join(EXPORT_DIR, "corrected_pdf")
        PDF_DIR = os.path.join(CORRECTED_ROOT, "pdf")
        CORR_DIR = os.path.join(CORRECTED_ROOT, "corrections")
        XLSX_PATH = os.path.join(CORR_DIR, "corrections.xlsx")
        os.makedirs(PDF_DIR, exist_ok=True)
        os.makedirs(CORR_DIR, exist_ok=True)

        if not os.path.exists(working_pdf_path):
            raise FileNotFoundError(f"Source PDF not found: {working_pdf_path}")

        doc = fitz.open(working_pdf_path)
        field_results = []

        class FieldProxy:
            class _DefaultFieldType:
                value = "text"
            def __init__(self, name, region, field_type=None):
                self.name = name
                self.region = region
                self.field_type = field_type or self._DefaultFieldType()

        for idx, reg in enumerate(regions):
            field_name = reg.get("field_name", f"field_{idx}")
            page_num = int(reg.get("page", 1)) - 1
            x = reg.get("x", 0)
            y = reg.get("y", 0)
            w = reg.get("w", 0)
            h = reg.get("h", 0)
            correction = reg.get("correction_value", "")

            field_proxy = FieldProxy(field_name, reg)
            extraction = ocr.extract_field(field_proxy, working_pdf_path)

            if correction and correction.strip():
                if 0 <= page_num < doc.page_count:
                    page = doc[page_num]
                    text = correction.strip()
                    rect = fitz.Rect(x, y, x + w, y + h)
                    page.draw_rect(rect, color=(1, 1, 1), fill=(1, 1, 1))

                    fixed_size = reg.get("fixed_text_size", False)
                    if fixed_size:
                        font_size = min(h * 0.75, 14)
                        font_size = max(font_size, 6)
                    else:
                        text_len = max(len(text), 1)
                        font_size = min(
                            (w - 6) / (text_len * 0.6),
                            h * 0.75,
                            14,
                        )
                        font_size = max(font_size, 6)

                    rc = page.insert_textbox(
                        rect, text, fontsize=font_size, fontname="helv",
                        color=(0, 0, 0), align=fitz.TEXT_ALIGN_LEFT, overlay=True,
                    )

                    if rc < 0:
                        if fixed_size:
                            page.insert_text(
                                rect.tl + fitz.Point(2, min(h * 0.7, 12)),
                                text, fontsize=font_size, fontname="helv",
                                color=(0, 0, 0), overlay=True,
                            )
                            logger.warning(
                                "Text overflow (fixed size) | field=%s | text='%s' | box=%.0fx%.0f | font=%.1f",
                                field_name, text, w, h, font_size,
                            )
                        else:
                            page.insert_text(
                                rect.tl + fitz.Point(2, min(h * 0.7, 12)),
                                text, fontsize=6, fontname="helv",
                                color=(0, 0, 0), overlay=True,
                            )
                            logger.warning(
                                "Text forced | field=%s | text='%s' | box=%.0fx%.0f",
                                field_name, text, w, h,
                            )

                    logger.info(
                        "Applied correction | field=%s | text='%s' | box=%.0fx%.0f@(%.0f,%.0f) | font=%.1f | rc=%d",
                        field_name, text, w, h, x, y, font_size, rc,
                    )

                    _update_corrections_xlsx(
                        XLSX_PATH,
                        serial_number=serial_number,
                        pdf_name=os.path.basename(original_filename or pdf_path),
                        field_name=field_name,
                        original_value=extraction.get("raw_value", ""),
                        corrected_value=correction,
                    )

            field_results.append({
                "field_name": field_name,
                "value": extraction.get("raw_value", ""),
                "corrected_value": correction,
                "extraction_method": extraction.get("guard_reason", "ocr"),
            })

        base_name = os.path.splitext(os.path.basename(original_filename or pdf_path))[0]
        out_pdf_name = f"{serial_number}_{base_name}.pdf"
        out_pdf_path = os.path.join(PDF_DIR, out_pdf_name)
        doc.save(out_pdf_path, garbage=4, deflate=True)
        doc.close()

        result_payload = {
            "status": "complete",
            "corrected_pdf": out_pdf_path,
            "fields_processed": len(field_results),
            "fields": field_results,
        }

        # For image originals, also render a corrected PNG
        if orig_ext in _IMAGE_EXTS:
            out_png_name = f"{serial_number}_{base_name}.png"
            out_png_path = os.path.join(PDF_DIR, out_png_name)
            _pdf_to_image(out_pdf_path, out_png_path)
            result_payload["corrected_image"] = out_png_path

        result_json_str = json.dumps(result_payload)

        from workers.pending_cache import save_pending_result, delete_pending
        save_pending_result(extraction_id, "complete", result_json_str)

        job.status = "complete"
        job.result_json = result_json_str
        job.completed_at = datetime.utcnow()
        job.error = None
        db.commit()
        delete_pending(extraction_id)

        logger.info(
            "Single OCR direct complete | id=%s | serial=%d | fields=%d | out=%s | orig=%s",
            extraction_id, serial_number, len(field_results), out_pdf_path,
            original_filename or pdf_path,
        )
        return result_payload

    except Exception as exc:
        logger.error("Single OCR direct failed | id=%s | err=%s", extraction_id, exc)
        if job:
            if self.request.retries >= self.max_retries:
                job.status = "failed"
                job.error = str(exc)
                db.commit()
                logger.error("All retries exhausted — marked as failed: %s", extraction_id)
            else:
                job.status = "processing"
                db.commit()
        raise self.retry(exc=exc, countdown=60)
    finally:
        db.close()


# ── Correction PDF export task ────────────────────────────────────────────────

@celery_app.task(
    name        = "workers.celery_app.apply_correction_to_pdf",
    bind        = True,
    max_retries = 3,
    acks_late   = True,
)
def apply_correction_to_pdf(
    self,
    correction_id:   str,
    extraction_id:   str,
    pdf_path:        str,
    field_name:      str,
    original_value:  str,
    corrected_value: str,
    serial_number:   int,
    original_filename: str,
):
    """
    Celery task — applies a human correction to a document.
    Supports PDF, Word and image originals. Non-PDF inputs are converted
    to PDF first, corrections applied, and image originals get a PNG render too.
    """
    import os
    from datetime import datetime

    EXPORT_DIR     = os.getenv("EXPORT_DIR", "export")
    CORRECTED_ROOT = os.path.join(EXPORT_DIR, "corrected_pdf")
    PDF_DIR        = os.path.join(CORRECTED_ROOT, "pdf")
    CORR_DIR       = os.path.join(CORRECTED_ROOT, "corrections")
    XLSX_PATH      = os.path.join(CORR_DIR, "corrections.xlsx")

    os.makedirs(PDF_DIR,  exist_ok=True)
    os.makedirs(CORR_DIR, exist_ok=True)

    try:
        import fitz
        import pdfplumber
        import openpyxl
        from openpyxl.styles import Font, PatternFill, Alignment, Border, Side

        working_pdf_path, orig_ext = _ensure_pdf(pdf_path, EXPORT_DIR)

        if not os.path.exists(working_pdf_path):
            raise FileNotFoundError(f"Source PDF not found: {working_pdf_path}")

        # 1. Locate original text bounding box via pdfplumber
        target_rect  = None
        target_page  = 0
        font_size    = 11

        with pdfplumber.open(working_pdf_path) as plumb:
            for page_num, page in enumerate(plumb.pages):
                words = page.extract_words(keep_blank_chars=True)
                text_on_page = " ".join(w["text"] for w in words)
                if original_value.strip() in text_on_page:
                    orig_words = original_value.strip().split()
                    i = 0
                    while i <= len(words) - len(orig_words):
                        span = words[i : i + len(orig_words)]
                        span_text = " ".join(w["text"] for w in span)
                        if span_text == original_value.strip():
                            x0 = min(w["x0"]   for w in span)
                            y0 = min(w["top"]   for w in span)
                            x1 = max(w["x1"]    for w in span)
                            y1 = max(w["bottom"] for w in span)
                            target_rect = (x0, y0, x1, y1)
                            target_page = page_num
                            font_size = round(y1 - y0)
                            break
                        i += 1
                    if target_rect:
                        break

        if not target_rect:
            logger.warning(
                "apply_correction_to_pdf: could not locate '%s' in %s — using fallback annotation",
                original_value, pdf_path,
            )
            target_rect = (36, 36, 400, 60)
            target_page = 0
            font_size   = 11

        # 2. Apply correction using PyMuPDF
        doc = fitz.open(working_pdf_path)
        page = doc[target_page]
        page_height = page.rect.height
        x0, y0_top, x1, y1_top = target_rect
        y0 = page_height - y1_top
        y1 = page_height - y0_top
        rect = fitz.Rect(x0, y0, x1, y1)

        page.draw_rect(rect, color=(1, 1, 1), fill=(1, 1, 1))
        page.insert_textbox(
            rect, corrected_value, fontsize=max(font_size, 6),
            fontname="helv", color=(0, 0, 0), align=fitz.TEXT_ALIGN_LEFT,
        )

        # 3. Save corrected PDF
        base_name = os.path.splitext(os.path.basename(original_filename or pdf_path))[0]
        out_pdf_name = f"{serial_number}_{base_name}.pdf"
        out_pdf_path = os.path.join(PDF_DIR, out_pdf_name)

        if os.path.exists(out_pdf_path):
            doc.close()
            doc = fitz.open(out_pdf_path)
            page = doc[target_page]
            page.draw_rect(rect, color=(1,1,1), fill=(1,1,1))
            page.insert_textbox(rect, corrected_value, fontsize=max(font_size,6),
                fontname="helv", color=(0,0,0), align=fitz.TEXT_ALIGN_LEFT)

        doc.save(out_pdf_path, garbage=4, deflate=True)
        doc.close()
        logger.info("Saved corrected PDF: %s", out_pdf_path)

        # For image originals, also render corrected PNG
        out_image_path = None
        if orig_ext in _IMAGE_EXTS:
            out_png_name = f"{serial_number}_{base_name}.png"
            out_image_path = os.path.join(PDF_DIR, out_png_name)
            _pdf_to_image(out_pdf_path, out_image_path)

        # 4. Update Excel corrections log
        _update_corrections_xlsx(
            XLSX_PATH, serial_number,
            os.path.basename(original_filename or pdf_path),
            field_name, original_value, corrected_value,
        )

        result = {
            "status":        "ok",
            "corrected_pdf": out_pdf_path,
            "xlsx":          XLSX_PATH,
        }
        if out_image_path:
            result["corrected_image"] = out_image_path
        return result

    except Exception as exc:
        logger.error("apply_correction_to_pdf failed for %s: %s", pdf_path, exc)
        raise self.retry(exc=exc, countdown=30)


def _update_corrections_xlsx(
    xlsx_path:       str,
    serial_number:   int,
    pdf_name:        str,
    field_name:      str,
    original_value:  str,
    corrected_value: str,
):
    """Append one row to corrections.xlsx. Creates with headers if missing."""
    import openpyxl
    from openpyxl.styles import Font, PatternFill, Alignment, Border, Side

    HEADERS = ["Serial No.", "PDF Name", "Field Name", "Original Text", "Correction"]

    if os.path.exists(xlsx_path):
        wb = openpyxl.load_workbook(xlsx_path)
        ws = wb.active
    else:
        wb = openpyxl.Workbook()
        ws = wb.active
        ws.title = "Corrections"
        header_fill   = PatternFill("solid", fgColor="1E40AF")
        header_font   = Font(bold=True, color="FFFFFF", size=11)
        header_align  = Alignment(horizontal="center", vertical="center", wrap_text=True)
        thin_border   = Border(
            left=Side(style="thin"), right=Side(style="thin"),
            top=Side(style="thin"), bottom=Side(style="thin"),
        )
        for col_num, header in enumerate(HEADERS, start=1):
            cell = ws.cell(row=1, column=col_num, value=header)
            cell.fill      = header_fill
            cell.font      = header_font
            cell.alignment = header_align
            cell.border    = thin_border
        col_widths = [12, 35, 25, 45, 45]
        for i, w in enumerate(col_widths, start=1):
            ws.column_dimensions[openpyxl.utils.get_column_letter(i)].width = w
        ws.row_dimensions[1].height = 30

    next_row = ws.max_row + 1
    data = [serial_number, pdf_name, field_name, original_value, corrected_value]
    row_align = Alignment(vertical="top", wrap_text=True)
    for col_num, value in enumerate(data, start=1):
        cell = ws.cell(row=next_row, column=col_num, value=value)
        cell.alignment = row_align
    if next_row % 2 == 0:
        row_fill = PatternFill("solid", fgColor="EFF6FF")
        for col_num in range(1, len(HEADERS) + 1):
            ws.cell(row=next_row, column=col_num).fill = row_fill
    wb.save(xlsx_path)

```

---

### `backend\workers\pending_cache.py`

```python
# backend/workers/pending_cache.py
#
# Protects against exactly one failure mode: OCR/extraction finishes
# successfully (the expensive part), but the Postgres write to persist
# that result fails because the network dropped at that exact moment.
# Without this, the computed result is just gone — the file has to be
# reprocessed entirely from scratch.
#
# Fix: write the result to a small local JSON file THE MOMENT it's
# computed, before ever attempting the DB write. If the DB write
# succeeds, delete the cache file immediately — this is the common case,
# adds one fast local disk write to every job, nothing more. If the DB
# write fails, the cache file survives, and flush_pending() (called on
# every startup, and periodically while running) retries pushing it to
# Postgres — without redoing any OCR — until it succeeds.
import os
import json
import glob
import logging

logger = logging.getLogger(__name__)

CACHE_DIR = os.path.join(os.getenv("EXPORT_DIR", "export"), "pending_results")


def save_pending_result(extraction_id: str, status: str, result_json: str, error: str | None = None) -> None:
    """Call this BEFORE attempting the DB write, right after computing the result."""
    os.makedirs(CACHE_DIR, exist_ok=True)
    path = os.path.join(CACHE_DIR, f"{extraction_id}.json")
    with open(path, "w") as f:
        json.dump({
            "extraction_id": extraction_id,
            "status": status,
            "result_json": result_json,
            "error": error,
        }, f)


def delete_pending(extraction_id: str) -> None:
    """Call this immediately after the DB write succeeds — the cache copy is no longer needed."""
    path = os.path.join(CACHE_DIR, f"{extraction_id}.json")
    try:
        os.remove(path)
    except FileNotFoundError:
        pass


def flush_pending() -> dict:
    """
    Retry writing every cached-but-unsaved result to Postgres. Does NOT
    redo any OCR/extraction — purely retries the persistence step for
    work that was already computed. Safe to call repeatedly; anything
    that succeeds gets its cache file deleted, anything that still fails
    (still offline) is left in place for the next flush attempt.
    """
    from models.db import init_db, SessionLocal, ExtractionJob
    from datetime import datetime

    init_db()
    os.makedirs(CACHE_DIR, exist_ok=True)

    flushed, failed = 0, 0
    for path in glob.glob(os.path.join(CACHE_DIR, "*.json")):
        try:
            with open(path) as f:
                cached = json.load(f)
        except (json.JSONDecodeError, OSError) as e:
            logger.error("flush_pending: unreadable cache file %s: %s", path, e)
            continue

        db = SessionLocal()
        try:
            job = db.query(ExtractionJob).filter_by(extraction_id=cached["extraction_id"]).first()
            if not job:
                logger.warning("flush_pending: job %s no longer exists, dropping cache", cached["extraction_id"])
                delete_pending(cached["extraction_id"])
                continue

            job.status = cached["status"]
            job.result_json = cached["result_json"]
            job.error = cached.get("error")
            job.completed_at = datetime.utcnow()
            db.commit()

            delete_pending(cached["extraction_id"])
            flushed += 1
            logger.info("flush_pending: recovered result for %s", cached["extraction_id"])
        except Exception as e:
            db.rollback()
            failed += 1
            logger.warning("flush_pending: still can't reach DB for %s: %s", cached["extraction_id"], e)
        finally:
            db.close()

    return {"flushed": flushed, "still_pending": failed}

```

---

### `backend\workers\recovery.py`

```python
# backend/workers/recovery.py
"""
Startup recovery script — called once by main.py lifespan on server start.

Reads export/checkpoint.json (written by Celery worker_shutdown signal)
and re-queues any extraction jobs interrupted during the last shutdown.

Safe to call even if no checkpoint file exists — simply returns.
"""
import json
import logging
import os
from datetime import datetime

from models.db import SessionLocal, ExtractionJob

logger          = logging.getLogger(__name__)
CHECKPOINT_FILE = os.path.join(os.getenv("EXPORT_DIR", "export"), "checkpoint.json")


def recover_incomplete_jobs():
    """Main entry point — called from main.py lifespan startup."""
    if not os.path.exists(CHECKPOINT_FILE):
        logger.info("No checkpoint file found — nothing to recover")
        return

    logger.info("Checkpoint found at %s — starting recovery...", CHECKPOINT_FILE)

    try:
        with open(CHECKPOINT_FILE) as f:
            checkpoint = json.load(f)
    except (json.JSONDecodeError, OSError) as e:
        logger.error("Could not read checkpoint file: %s", e)
        return

    incomplete = checkpoint.get("incomplete_jobs", [])
    if not incomplete:
        logger.info("Checkpoint is empty — nothing to recover")
        _cleanup_checkpoint()
        return

    logger.info("Found %d interrupted job(s) to recover", len(incomplete))

    # Lazy import to avoid circular imports at module load time
    from workers.celery_app import process_document

    db        = SessionLocal()
    recovered = 0
    skipped   = 0

    try:
        for item in incomplete:
            extraction_id = item.get("extraction_id")
            if not extraction_id:
                continue

            job = db.query(ExtractionJob).filter_by(
                extraction_id=extraction_id
            ).first()

            if not job:
                logger.warning(
                    "Job %s in checkpoint but not in DB — skipping", extraction_id
                )
                skipped += 1
                continue

            if job.status in ("complete", "failed"):
                logger.info(
                    "Job %s already %s — skipping", extraction_id, job.status
                )
                skipped += 1
                continue

            if not job.pdf_path or not job.schema_id or not job.doc_hash:
                logger.warning(
                    "Job %s missing required fields — marking failed", extraction_id
                )
                job.status = "failed"
                job.error  = "Missing required fields at recovery time"
                db.commit()
                skipped += 1
                continue

            # FIX: use utcnow consistently (not datetime.now())
            # Reset to pending so UI shows correct state
            job.status     = "pending"
            job.started_at = None
            db.commit()

            # Re-enqueue to Celery — acks_late ensures it won't be lost again
            process_document.delay(
                job.extraction_id,
                job.pdf_path,
                job.schema_id,
                job.doc_hash,
            )
            logger.info("Re-queued: %s", extraction_id)
            recovered += 1

    except Exception as e:
        logger.error("Recovery error: %s", e)
    finally:
        db.close()

    logger.info(
        "Recovery complete — %d re-queued, %d skipped", recovered, skipped
    )
    _cleanup_checkpoint()


def _cleanup_checkpoint():
    """Remove checkpoint file after successful recovery."""
    try:
        os.remove(CHECKPOINT_FILE)
        logger.info("Checkpoint file removed")
    except OSError:
        pass

```

---
