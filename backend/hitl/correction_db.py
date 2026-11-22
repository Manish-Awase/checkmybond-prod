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
