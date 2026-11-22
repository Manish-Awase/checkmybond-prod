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
    from workers.celery_app import process_document, process_parsed_content

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

            if not job.schema_id or not job.doc_hash:
                logger.warning(
                    "Job %s missing required fields — marking failed", extraction_id
                )
                job.status = "failed"
                job.error  = "Missing required fields at recovery time"
                db.commit()
                skipped += 1
                continue

            job.status     = "pending"
            job.started_at = None
            db.commit()

            if job.pdf_path:
                # Server-held PDF (website upload flow) — re-run the
                # full pipeline starting from the file.
                process_document.delay(
                    job.extraction_id,
                    job.pdf_path,
                    job.schema_id,
                    job.doc_hash,
                )
                logger.info("Re-queued (server pdf): %s", extraction_id)
                recovered += 1

            elif job.parsed_content_json:
                # Desktop LLM-mode job — the PDF never reached this
                # server, but the parsed content did and is persisted.
                # Re-run starting from that.
                process_parsed_content.delay(
                    job.extraction_id,
                    job.schema_id,
                    job.doc_hash,
                )
                logger.info("Re-queued (desktop parsed content): %s", extraction_id)
                recovered += 1

            else:
                # No pdf_path and no parsed_content_json — this is a
                # desktop OCR-mode job. Those are computed entirely
                # client-side and pushed as a finished result; there is
                # no server-side Celery work to resume for them.
                logger.info(
                    "Job %s has neither pdf_path nor parsed_content_json — "
                    "likely a desktop OCR job with nothing pending server-side, skipping",
                    extraction_id,
                )
                skipped += 1

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