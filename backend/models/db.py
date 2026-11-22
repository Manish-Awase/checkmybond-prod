# backend/models/db.py
import os
from pathlib import Path
from sqlalchemy import (
    create_engine, Column, String, Integer, Float,
    Text, DateTime, Boolean, ForeignKey, event, UniqueConstraint, Index, func
)
from sqlalchemy.dialects.postgresql import JSONB
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


# ── Organizations (multi-tenancy root) ──────────────────────────────────────

class Organization(Base):
    """
    Tenant record. users.org_id and template_definitions.org_id both FK to
    this table's org_id — NOT to users.org_id directly, since many users
    share one org_id and a foreign key target must be unique. This table
    is that single, actually-unique tenant key.
    """
    __tablename__ = "organizations"

    org_id     = Column(String, primary_key=True)
    org_name   = Column(String, nullable=False)
    created_at = Column(DateTime, default=datetime.utcnow)


# ── Users ─────────────────────────────────────────────────────────────────────

class User(Base):
    __tablename__ = "users"
    # EMAIL is the only unique identity on a user. The username is just a
    # display name -- any number of accounts may share one -- so it has NO
    # unique constraint, and nothing may look an account up by it (login
    # is by email; see hitl-go/routers/auth_identity.go).
    #
    # uq_users_email is the exact-match constraint; ux_users_email_lower
    # makes it case-insensitive ("A@x.com" and "a@x.com" are one address),
    # which is also what hitl-go's GetUserByEmail matches on. An existing
    # database gets both from migrations/0006_username_not_unique.sql;
    # create_all() only builds them for a fresh one.
    __table_args__ = (
        UniqueConstraint("email", name="uq_users_email"),
    )

    id            = Column(String,      primary_key=True)
    # Tenant this user belongs to. Nullable for now — CreateUser in
    # hitl-go/internal/db/db.go does not currently set this column, so
    # making it NOT NULL here would break every signup until that INSERT
    # is updated to supply an org_id. Flip to nullable=False once the
    # signup flow (routers/auth.go, register pages) always assigns one.
    org_id        = Column(String, ForeignKey("organizations.org_id"), index=True, nullable=True)
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


# Case-insensitive uniqueness for email -- see the comment on User.__table_args__.
Index("ux_users_email_lower", func.lower(User.email), unique=True)


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
    # Desktop-sourced jobs (LLM mode): pdfplumber-parsed pages/tables JSON,
    # POSTed by the Tauri app to /jobs/{id}/parsed-content. Populated
    # instead of pdf_path for these jobs — no PDF ever reaches this server
    # for that flow. NULL for website-uploaded jobs and desktop OCR-mode
    # jobs (the latter push a finished result directly, nothing parses
    # server-side). See workers/celery_app.py's process_parsed_content
    # and workers/recovery.py, which branch on pdf_path vs this column.
    parsed_content_json = Column(Text, nullable=True)
    status        = Column(String,  default="pending")
    result_json   = Column(Text)
    error         = Column(Text)
    # FIX: was datetime.now() — evaluated once at import time (frozen timestamp)
    created_at    = Column(DateTime, default=datetime.utcnow)
    started_at    = Column(DateTime, nullable=True)
    completed_at  = Column(DateTime, nullable=True)
    sla_deadline  = Column(DateTime, nullable=False,
                           default=datetime.utcnow)  # set properly on job creation


# ── Field Library (bond reconciliation field definitions) ──────────────────────
# org_id NULL = built-in field, visible to every org (critical/standard).
# org_id set = an org's own custom field. Seed data below is illustrative
# starting content for common bond terms, not an authoritative bond-domain
# field list — edit/replace before relying on it.

# ── Field Library (global bond field definitions, no org-scoping) ──────────────
# Superseded design vs. my earlier org-scoped Field/fields: nothing in the
# actual executable code (reconciliation_engine.py, fields.go) scopes
# fields by org — FieldLibrary is intentionally global. field_type is
# used both as the data type AND as the reconciliation-engine's
# normalization key (normalize_value() switches on it) — keep values to
# text | number | percentage | date | currency | boolean.

class FieldLibrary(Base):
    __tablename__ = "field_library"

    id          = Column(String, primary_key=True)
    name        = Column(String, nullable=False, index=True)
    description = Column(Text, nullable=True)
    field_type  = Column(String(32), nullable=False)
    aliases     = Column(JSONB, default=list)  # list[str]
    category    = Column(String(32), default="custom")  # critical | standard | custom
    is_active   = Column(Boolean, default=True)
    created_at  = Column(DateTime, default=datetime.utcnow)
    updated_at  = Column(DateTime, default=datetime.utcnow, onupdate=datetime.utcnow)


# ── Template Definitions (per-org document fingerprint + rules cache) ──────────

class TemplateDefinition(Base):
    """
    Per-organization document 'fingerprint' + extraction rules cache — lets
    a previously-seen bank/document_type/layout combination reuse
    already-validated field rules instead of re-deriving them from scratch.
    org_id scopes every row to one tenant; a row from one org's cache must
    never be matched against or returned for another org's documents —
    every query against this table needs org_id in the WHERE clause, not
    just in the index.

    This schema is confirmed correct against the ACTUAL executable logic
    in services/template_matcher.py and services/template_learning.py —
    both query/write template_id, org_id, bank_name, document_type,
    fingerprint_hash, success_count, fail_count exactly as defined here.
    (A separate db_additions.py in the same upload defined a different,
    incompatible TemplateDefinition with no org_id/bank_name and `id` as
    the PK name — that version is NOT used anywhere and was discarded.)

    fingerprint_json / rules_json / field_ids_json use JSONB, which
    requires Postgres — this will fail against the sqlite fallback
    DATABASE_URL defined near the top of this file.
    """
    __tablename__ = "template_definitions"

    template_id      = Column(String, primary_key=True)
    org_id           = Column(String, ForeignKey("organizations.org_id"), nullable=False, index=True)
    template_name    = Column(String, nullable=True)
    bank_name        = Column(String, nullable=False)
    document_type    = Column(String, nullable=False)  # prospectus | term_sheet | confirmation
    fingerprint_hash = Column(String, nullable=False)
    fingerprint_json = Column(JSONB,  nullable=False)
    rules_json       = Column(JSONB,  nullable=False)  # {field_name: [rule, ...]}
    field_ids_json   = Column(JSONB, nullable=True)    # list[str] of field_library.id
    created_by       = Column(String, ForeignKey("users.id"), nullable=False)
    created_at       = Column(DateTime(timezone=True), default=datetime.utcnow)
    updated_at       = Column(DateTime(timezone=True), default=datetime.utcnow, onupdate=datetime.utcnow)
    last_used_at     = Column(DateTime(timezone=True), nullable=True)  # for GC, not correctness
    success_count    = Column(Integer, default=0)  # times these rules succeeded
    fail_count       = Column(Integer, default=0)  # times these rules failed (drift signal)
    is_active        = Column(Boolean, default=True)

    __table_args__ = (
        Index(
            "idx_templates_org_fingerprint",
            "org_id", "bank_name", "document_type", "fingerprint_hash",
        ),
        Index(
            "idx_templates_org_active",
            "org_id", "is_active", "last_used_at",
        ),
    )


# ── Bond Sessions ─────────────────────────────────────────────────────────────
# Adopted from db_additions.py's BondSession — this shape is what
# templates.go (annotations), template_learning.py (documents,
# annotations, template_id), and reconcile.go (extraction_results,
# reconciliation_result) all actually read/write. Dropped org_id: nothing
# in this batch queries it on this table (org is always resolved via
# user_id -> users.org_id when needed, e.g. in templates_preflight.go).
# All JSON-shaped columns are JSONB, not Text — routers/reconcile.go's
# handleConfirmReconciliation uses Postgres's jsonb_set() on
# reconciliation_result, which requires an actual jsonb column type.

class BondSession(Base):
    __tablename__ = "bond_sessions"

    session_id            = Column(String, primary_key=True)
    user_id               = Column(String, ForeignKey("users.id"), nullable=False, index=True)
    status                = Column(String, default="parsing")
    documents             = Column(JSONB, nullable=True)   # [{doc_type, filename, pdf_path, doc_hash, parsed_json?}]
    selected_field_ids    = Column(JSONB, nullable=True)   # list[str]
    annotations           = Column(JSONB, nullable=True)   # [{field_id, field_name, page, value, surrounding_text, section_heading, ...}]
    template_id           = Column(String, ForeignKey("template_definitions.template_id"), nullable=True, index=True)
    extraction_results    = Column(JSONB, nullable=True)   # {doc_type: {field_name: {value, confidence, evidence, rule_used, edited, edited_at}}}
    reconciliation_result = Column(JSONB, nullable=True)   # {status, fields: [...]}
    created_at            = Column(DateTime, default=datetime.utcnow)
    updated_at            = Column(DateTime, default=datetime.utcnow, onupdate=datetime.utcnow)


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


class LoginEvent(Base):
    """One row per successful login (written by the Go server). Powers the
    daily/weekly/monthly active-user queries in database/usage_queries.sql."""
    __tablename__ = "login_events"

    id           = Column(Integer, primary_key=True, autoincrement=True)
    user_id      = Column(String, nullable=False)
    logged_in_at = Column(DateTime, nullable=False, default=datetime.utcnow, server_default=func.now())

    __table_args__ = (
        Index("ix_login_events_logged_in_at", "logged_in_at"),
        Index("ix_login_events_user_id_logged_in_at", "user_id", "logged_in_at"),
    )


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
