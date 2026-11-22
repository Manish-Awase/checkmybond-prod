#!/usr/bin/env python3
# backend/workers/cli_desktop.py
#
# Desktop entrypoint: runs Python services LOCALLY on the user's machine.
# NO database connection. NO Redis. NO Qdrant. NO .env file needed.
# NO LLM key -- this only ever does pdfplumber parsing, never an LLM call.
#
# LLM-mode only -- OCR mode was removed. If you need OCR mode back, restore
# process_document_desktop and RegionOCRExtractor from git history rather
# than reintroducing them here from scratch.
#
# Reads one job JSON from stdin, writes one result JSON to stdout.
#
# task_type "parse_document_local": runs ONLY the pdfplumber parse (no LLM
#   call, no key, no network) via ParserService.parse_digital_only() and
#   returns the parsed pages/tables plus a locally-computed doc_hash. The
#   caller (the Tauri frontend) registers a schema, creates a desktop job,
#   then POSTs this result to the server's /jobs/{id}/parsed-content
#   endpoint, which does the embedding/Qdrant/Groq work server-side.
#   Scanned PDFs raise cleanly here rather than silently falling back to
#   a Textract network call -- see ParserService.parse_digital_only().

import hashlib
import json
import logging
import os
import sys
import traceback

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from services.parser_service import ParserService

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s | %(levelname)s | %(message)s",
    stream=sys.stderr,
)
logger = logging.getLogger("cli_desktop")


def main():
    try:
        job = json.load(sys.stdin)
    except json.JSONDecodeError as e:
        json.dump({"status": "error", "error": f"Invalid JSON on stdin: {e}"}, sys.stdout)
        sys.exit(1)

    task_type = job.get("task_type")
    extraction_id = job.get("extraction_id", "desktop-unknown")
    logger.info("Received job | task_type=%s extraction_id=%s", task_type, extraction_id)

    try:
        if task_type == "parse_document_local":
            result = parse_document_local(job)
        else:
            raise ValueError(f"Unknown task_type: {task_type}")

        json.dump({"status": "success", "result": result}, sys.stdout)
        logger.info("Job completed | extraction_id=%s", extraction_id)

    except Exception as e:
        err_msg = f"{type(e).__name__}: {e}"
        logger.error("Job failed | extraction_id=%s | error=%s\n%s",
                     extraction_id, err_msg, traceback.format_exc())
        json.dump({"status": "error", "error": err_msg, "traceback": traceback.format_exc()}, sys.stdout)
        sys.exit(1)


def _hash_file(path: str) -> str:
    """SHA-256 of file contents, computed locally so the frontend never
    needs its own hashing dependency."""
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(8192), b""):
            h.update(chunk)
    return h.hexdigest()


def parse_document_local(job: dict) -> dict:
    """
    LLM-mode: runs ONLY the pdfplumber parse locally, digital-PDF-only.
    No LLM key, no network call, no Qdrant, no AWS. doc_hash is computed
    here (not passed in) so the caller doesn't need its own hashing.
    Returns the parsed pages/tables for the caller to POST to the
    server's parsed-content endpoint.
    """
    pdf_path = job.get("pdf_path")
    extraction_id = job.get("extraction_id", "desktop-unknown")

    if not pdf_path:
        raise ValueError("pdf_path is required")
    if not os.path.exists(pdf_path):
        raise FileNotFoundError(f"PDF not found: {pdf_path}")

    doc_hash = _hash_file(pdf_path)

    parser = ParserService()
    parsed = parser.parse_digital_only(pdf_path, doc_hash)

    return {
        "extraction_id": extraction_id,
        "doc_hash": doc_hash,
        "filename": os.path.basename(pdf_path),
        "parsed": parsed,
    }


if __name__ == "__main__":
    main()
