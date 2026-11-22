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
