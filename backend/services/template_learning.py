# backend/services/template_learning.py
"""
Template Learning Engine v2.
Generates 3-5 text-based extraction rules per field from human annotations.
Saves templates with deterministic fingerprint_hash for fast O(1) matching.
"""
import hashlib
import json
import logging
import re
from datetime import datetime
from typing import List, Dict, Any

from services.llm_client import complete
from models.db import SessionLocal, TemplateDefinition, BondSession

logger = logging.getLogger("template_learning")

RULE_GENERATION_PROMPT = """You are a precise financial document extraction rule engine.

Given a field annotation from a bond document, generate 3-5 extraction rules.
Each rule must be a simple text-based strategy that can be executed without coordinates.

Field: {field_name}
Field Type: {field_type}
Section Heading: {section}
Annotated Text (evidence): {evidence}
Surrounding Context: {context}

Generate rules in this EXACT JSON format:
[
  {
    "priority": 1,
    "strategy": "section_heading",
    "description": "Look under section '{section}' for line starting with '{field_name}:'",
    "pattern": "regular expression pattern to extract value",
    "fallback": "what to try if this fails"
  },
  ...
]

Strategies to use (pick the best 3-5):
1. section_heading — look under a specific section heading
2. table_row — find row in table where first column matches field/alias
3. nearby_keyword — find keyword within N chars of field name
4. label_value_pair — match "Field Name: value" pattern
5. sentence_pattern — extract from sentence containing field name

Rules MUST:
- Use only text matching (regex, string search)
- Never reference page coordinates or bounding boxes
- Be ordered by priority (1 = most reliable)
- Include the actual regex pattern that can be executed in Python

Return ONLY a JSON array. No markdown, no explanation."""


class TemplateLearningEngine:
    def __init__(self):
        pass

    def generate_rules_from_session(self, session_id: str, org_id: str) -> Dict[str, Any]:
        """org_id required, no default — see template_matcher.py's
        find_best_template for why a magic fallback string is unsafe here
        (would create templates scoped to a nonexistent org, invisible to
        every real org's future matching)."""
        """Main entry: read annotations from bond session, generate rules, save template."""
        db = SessionLocal()
        try:
            session = db.query(BondSession).filter_by(session_id=session_id).first()
            if not session:
                return {"status": "error", "message": "session not found"}

            annotations = json.loads(session.annotations) if session.annotations else []
            if not annotations:
                return {"status": "error", "message": "no annotations found"}

            # Group by field_name (present in the raw annotation as sent
            # by AnnotationPage.tsx). "Best" annotation per field is the
            # one with the most surrounding_text, since that's the actual
            # key the frontend sends -- not "evidence", which doesn't
            # exist in the frontend's annotation shape.
            by_field: Dict[str, List[dict]] = {}
            for ann in annotations:
                fn = ann.get("field_name", "unknown")
                by_field.setdefault(fn, []).append(ann)

            # field_type isn't sent by the frontend either (AnnotationPage
            # only tracks field_id/field_name) -- resolve it from
            # field_library via field_id, defaulting to "text" if the
            # field was deleted or field_id is missing.
            field_ids_seen = {a.get("field_id") for a in annotations if a.get("field_id")}
            field_types: Dict[str, str] = {}
            if field_ids_seen:
                from models.db import FieldLibrary
                for f in db.query(FieldLibrary).filter(FieldLibrary.id.in_(field_ids_seen)).all():
                    field_types[f.id] = f.field_type

            rules_by_field = {}
            for field_name, anns in by_field.items():
                best = max(anns, key=lambda a: len(a.get("surrounding_text", "")))
                best_field_type = field_types.get(best.get("field_id"), "text")
                rules = self._generate_rules_for_field(best, best_field_type)
                rules_by_field[field_name] = rules

            fingerprint = self._build_fingerprint(session)
            field_ids = list({a.get("field_id", "") for a in annotations if a.get("field_id")})

            template_id = self._save_template(db, session, fingerprint, rules_by_field, field_ids, org_id)

            session.status = "learned"
            session.template_id = template_id
            db.commit()

            return {
                "status": "success",
                "template_id": template_id,
                "fields": len(rules_by_field),
                "rules_total": sum(len(r) for r in rules_by_field.values()),
            }

        except Exception as e:
            db.rollback()
            logger.error("Template learning failed: %s", e)
            return {"status": "error", "message": str(e)}
        finally:
            db.close()

    def _generate_rules_for_field(self, annotation: dict, field_type: str = "text") -> List[Dict[str, Any]]:
        field_name = annotation.get("field_name", "")
        section = annotation.get("section_heading", "")
        evidence = annotation.get("value", "")
        context = annotation.get("surrounding_text", evidence)[:800]

        prompt = RULE_GENERATION_PROMPT.format(
            field_name=field_name,
            field_type=field_type,
            section=section,
            evidence=evidence[:400],
            context=context,
        )

        try:
            raw = complete(prompt, max_tokens=1024, temperature=0.1).strip()
            raw = re.sub(r"^```json\s*", "", raw)
            raw = re.sub(r"```\s*$", "", raw)
            rules = json.loads(raw)
            if not isinstance(rules, list):
                rules = [rules]
            cleaned = []
            for i, r in enumerate(rules[:5]):
                if isinstance(r, dict):
                    r["priority"] = i + 1
                    r["source"] = "llm_generated"
                    cleaned.append(r)
            return cleaned
        except Exception as e:
            logger.warning("LLM rule generation failed for %s: %s", field_name, e)
            return self._fallback_rules(field_name, evidence, section)

    def _fallback_rules(self, field_name: str, evidence: str, section: str) -> List[Dict[str, Any]]:
        rules = []
        if section:
            rules.append({
                "priority": 1,
                "strategy": "section_heading",
                "description": f"Look under section '{section}' for '{field_name}'",
                "pattern": rf"(?i){re.escape(field_name)}[:\s]+([^\n]+)",
                "fallback": "search entire document",
            })
        rules.append({
            "priority": 2,
            "strategy": "label_value_pair",
            "description": f"Match '{field_name}:' followed by value",
            "pattern": rf"(?i){re.escape(field_name)}[:\s]+([^\n,;]+)",
            "fallback": "fuzzy keyword search",
        })
        rules.append({
            "priority": 3,
            "strategy": "nearby_keyword",
            "description": f"Find paragraph containing '{field_name}'",
            "pattern": rf"(?i).{{0,50}}{re.escape(field_name)}.{{0,200}}",
            "fallback": "human review",
        })
        return rules

    def _build_fingerprint(self, session) -> Dict[str, Any]:
        documents = json.loads(session.documents) if session.documents else []
        fingerprint = {
            "heading_sequence": [],
            "table_signatures": [],
            "bank_name": None,
            "doc_types": [],
        }

        for doc in documents:
            fingerprint["doc_types"].append(doc.get("doc_type", "unknown"))
            parsed = doc.get("parsed_json", {})
            if isinstance(parsed, dict):
                fingerprint["heading_sequence"].extend([
                    h.get("heading", "") for h in parsed.get("heading_hierarchy", [])
                ])
                fingerprint["table_signatures"].extend([
                    ts.get("header_fingerprint", "") for ts in parsed.get("table_signatures", [])
                ])

        for heading in fingerprint["heading_sequence"]:
            m = re.search(r"([A-Z][a-zA-Z\s&]+(?:Bank|Securities|Group|Capital|Markets))", heading)
            if m:
                fingerprint["bank_name"] = m.group(1).strip()
                break

        return fingerprint

    def _save_template(self, db, session, fingerprint, rules_by_field, field_ids, org_id: str) -> str:
        import uuid
        template_id = str(uuid.uuid4())

        bank = fingerprint.get("bank_name", "Unknown")
        doc_types = fingerprint.get("doc_types", ["prospectus"])
        doc_type = doc_types[0] if doc_types else "prospectus"

        # Build canonical hash for fast matching
        canonical = {
            "bank_name": bank,
            "doc_type": doc_type,
            "heading_sequence": fingerprint.get("heading_sequence", [])[:20],
            "table_signatures": fingerprint.get("table_signatures", [])[:5],
        }
        fp_hash = hashlib.sha256(json.dumps(canonical, sort_keys=True).encode()).hexdigest()[:32]

        template = TemplateDefinition(
            template_id=template_id,
            org_id=org_id,
            bank_name=bank,
            document_type=doc_type,
            fingerprint_hash=fp_hash,
            fingerprint_json=fingerprint,      # JSONB column, dict — correct as-is
            rules_json=rules_by_field,         # JSONB column, dict — correct as-is
            field_ids_json=field_ids,          # JSONB column expects a list, not json.dumps(field_ids)
                                                # (a JSON-encoded string would be stored as a scalar
                                                # string value, not an array — every later reader
                                                # that expects a list would break)
            created_by=session.user_id if hasattr(session, 'user_id') else 'system',
            confidence_score=0.0,
            success_count=0,
            fail_count=0,
            is_active=True,
        )
        db.add(template)
        db.commit()
        return template_id

    def retrain_template(self, template_id: str, annotations: List[dict]) -> Dict[str, Any]:
        db = SessionLocal()
        try:
            template = db.query(TemplateDefinition).filter_by(template_id=template_id).first()
            if not template:
                return {"status": "error", "message": "template not found"}

            existing_rules = template.rules_json if isinstance(template.rules_json, dict) else json.loads(template.rules_json)

            field_ids_seen = {a.get("field_id") for a in annotations if a.get("field_id")}
            field_types: Dict[str, str] = {}
            if field_ids_seen:
                from models.db import FieldLibrary
                for f in db.query(FieldLibrary).filter(FieldLibrary.id.in_(field_ids_seen)).all():
                    field_types[f.id] = f.field_type

            for ann in annotations:
                fn = ann.get("field_name", "")
                rules = self._generate_rules_for_field(ann, field_types.get(ann.get("field_id"), "text"))
                if fn in existing_rules:
                    existing_rules[fn] = rules + existing_rules[fn][-2:]
                else:
                    existing_rules[fn] = rules

            template.rules_json = existing_rules
            template.updated_at = datetime.utcnow()
            db.commit()

            return {
                "status": "retrained",
                "template_id": template_id,
                "fields_updated": len(annotations),
            }
        finally:
            db.close()
