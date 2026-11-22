# backend/services/template_matcher_preflight.py
"""
Preflight fingerprinting service.

REQUIRES services/bond_extractor.py (BondDocumentParser) and
services/template_matcher.py (TemplateMatcher, MATCH_THRESHOLD) — NEITHER
EXISTS YET. This file will raise ImportError until both are supplied;
their actual parsing/fingerprinting/matching logic is product-specific
business logic this integration pass does not fabricate.

Column references below are corrected to match the real
TemplateDefinition model in models/db.py (template_id as primary key,
not id; org_id FK to organizations, not orgs).
"""
import json
import logging
import hashlib
from typing import Dict, Any

from services.bond_extractor import BondDocumentParser      # NOT YET PROVIDED
from services.template_matcher import TemplateMatcher, MATCH_THRESHOLD  # NOT YET PROVIDED
from models.db import SessionLocal, TemplateDefinition

logger = logging.getLogger("template_matcher_preflight")


class PreflightFingerprinter:
    """
    One-shot fingerprint + match for a single uploaded document.
    Returns match result, drift stats, and auto-extract recommendation.
    """

    def __init__(self):
        self.parser = BondDocumentParser()
        self.matcher = TemplateMatcher()

    def preflight(self, pdf_path: str, doc_type: str, org_id: str) -> Dict[str, Any]:
        doc_hash = self._hash_file(pdf_path)
        parsed = self.parser.parse(pdf_path, doc_hash)

        fingerprint = self.matcher.fingerprinter.fingerprint(parsed)
        fingerprint_hash = self._hash_fingerprint(fingerprint)

        db = SessionLocal()
        try:
            exact = db.query(TemplateDefinition).filter_by(
                org_id=org_id,
                document_type=doc_type,
                fingerprint_hash=fingerprint_hash,
                is_active=True,
            ).first()

            if exact:
                rules = exact.rules_json or {}  # JSONB -> already a native dict, no json.loads needed
                return {
                    "status": "matched",
                    "match_type": "exact_hash",
                    "template_id": exact.template_id,
                    "template_name": exact.template_name,
                    "score": 1.0,
                    "can_auto_extract": True,
                    "fingerprint_hash": fingerprint_hash,
                    "bank_name": fingerprint.get("bank_name"),
                    "drift": {
                        "success_count": exact.success_count,
                        "fail_count": exact.fail_count,
                        "total_uses": exact.success_count + exact.fail_count,
                        "drift_score": exact.fail_count / max(1, exact.success_count + exact.fail_count),
                    },
                    "rules_count": len(rules),
                }

            best = self.matcher.find_best_template(parsed, org_id)
            if best and best.get("score", 0) >= MATCH_THRESHOLD:
                tmpl = db.query(TemplateDefinition).filter_by(template_id=best["template_id"]).first()
                if tmpl:
                    from datetime import datetime
                    tmpl.last_used_at = datetime.utcnow()
                    tmpl.updated_at = datetime.utcnow()
                    db.commit()

                return {
                    "status": "matched",
                    "match_type": "fuzzy",
                    "template_id": best["template_id"],
                    "template_name": best["template_name"],
                    "score": best["score"],
                    "can_auto_extract": True,
                    "fingerprint_hash": fingerprint_hash,
                    "bank_name": fingerprint.get("bank_name"),
                    "drift": {
                        "success_count": tmpl.success_count if tmpl else 0,
                        "fail_count": tmpl.fail_count if tmpl else 0,
                        "total_uses": (tmpl.success_count + tmpl.fail_count) if tmpl else 0,
                        "drift_score": tmpl.fail_count / max(1, tmpl.success_count + tmpl.fail_count) if tmpl else 0,
                    },
                    "rules_count": len(best.get("rules", {})),
                }

            return {
                "status": "no_match",
                "match_type": "none",
                "template_id": None,
                "score": 0.0,
                "can_auto_extract": False,
                "fingerprint_hash": fingerprint_hash,
                "bank_name": fingerprint.get("bank_name"),
                "heading_count": len(fingerprint.get("heading_sequence", [])),
                "table_signature_count": len(fingerprint.get("table_signatures", [])),
                "message": "No matching template found. Please annotate 1-3 documents to create a template.",
            }

        finally:
            db.close()

    def _hash_file(self, path: str) -> str:
        h = hashlib.sha256()
        with open(path, "rb") as f:
            for chunk in iter(lambda: f.read(8192), b""):
                h.update(chunk)
        return h.hexdigest()[:32]

    def _hash_fingerprint(self, fp: dict) -> str:
        canonical = json.dumps(fp, sort_keys=True)
        return hashlib.sha256(canonical.encode()).hexdigest()[:16]
