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
