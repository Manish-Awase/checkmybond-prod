# backend/services/template_matcher.py
"""
Template Matching Engine v2.
Fingerprints incoming documents and matches against saved templates.
Two-tier matching: exact fingerprint hash (O(1)) → fuzzy similarity fallback.
"""
import hashlib
import json
import logging
import re
from typing import List, Dict, Any, Optional

from models.db import SessionLocal, TemplateDefinition

logger = logging.getLogger("template_matcher")

MATCH_THRESHOLD = 0.85


class DocumentFingerprinter:
    """Creates a fingerprint from a parsed bond document."""

    def fingerprint(self, parsed_doc: dict) -> Dict[str, Any]:
        fp = {
            "heading_sequence": [],
            "table_signatures": [],
            "bank_name": None,
            "doc_types": [parsed_doc.get("doc_type", "unknown")],
        }

        for h in parsed_doc.get("heading_hierarchy", []):
            fp["heading_sequence"].append(h.get("heading", ""))

        for ts in parsed_doc.get("table_signatures", []):
            fp["table_signatures"].append(ts.get("header_fingerprint", ""))

        # Detect bank name
        for heading in fp["heading_sequence"]:
            m = re.search(r"([A-Z][a-zA-Z\s&]+(?:Bank|Securities|Group|Capital|Markets))", heading)
            if m:
                fp["bank_name"] = m.group(1).strip()
                break

        return fp


class TemplateMatcher:
    """Matches document fingerprints against template library."""

    def __init__(self):
        self.fingerprinter = DocumentFingerprinter()

    def find_best_template(self, parsed_doc: dict, org_id: str) -> Optional[Dict[str, Any]]:
        """
        org_id is required, no default — a template row scoped to the
        wrong (or a nonexistent) org would be a real cross-tenant data
        leak, so callers must resolve a real org_id before calling this,
        not rely on a magic fallback string.
        """
        doc_fp = self.fingerprinter.fingerprint(parsed_doc)
        bank = doc_fp.get("bank_name", "Unknown")
        doc_type = doc_fp.get("doc_types", ["prospectus"])[0]

        # Build deterministic hash of canonical fingerprint
        canonical = {
            "bank_name": bank,
            "doc_type": doc_type,
            "heading_sequence": doc_fp.get("heading_sequence", [])[:20],
            "table_signatures": doc_fp.get("table_signatures", [])[:5],
        }
        fp_string = json.dumps(canonical, sort_keys=True)
        fp_hash = hashlib.sha256(fp_string.encode()).hexdigest()[:32]

        db = SessionLocal()
        try:
            # --- TIER 1: Exact hash match ---
            exact = db.query(TemplateDefinition).filter_by(
                org_id=org_id,
                bank_name=bank,
                document_type=doc_type,
                fingerprint_hash=fp_hash,
                is_active=True,
            ).first()

            if exact:
                rules = exact.rules_json if isinstance(exact.rules_json, dict) else json.loads(exact.rules_json)
                return {
                    "template_id": exact.template_id,
                    "template_name": f"{exact.bank_name} {exact.document_type}",
                    "score": 1.0,
                    "match_type": "exact_hash",
                    "rules": rules,
                    "field_ids": exact.field_ids_json or [],  # JSONB -> already a native list, no json.loads needed
                }

            # --- TIER 2: Fuzzy fallback ---
            templates = db.query(TemplateDefinition).filter_by(
                org_id=org_id, is_active=True
            ).limit(50).all()

            best_match = None
            best_score = 0.0

            for tmpl in templates:
                tmpl_fp = tmpl.fingerprint_json if isinstance(tmpl.fingerprint_json, dict) else json.loads(tmpl.fingerprint_json)
                score = self._compute_similarity(doc_fp, tmpl_fp)
                if score > best_score:
                    best_score = score
                    best_match = tmpl

            if best_match and best_score >= MATCH_THRESHOLD:
                # Update hash for next time (learn the fast path)
                best_match.fingerprint_hash = fp_hash
                db.commit()

                rules = best_match.rules_json if isinstance(best_match.rules_json, dict) else json.loads(best_match.rules_json)
                return {
                    "template_id": best_match.template_id,
                    "template_name": f"{best_match.bank_name} {best_match.document_type}",
                    "score": round(best_score, 4),
                    "match_type": "fuzzy",
                    "rules": rules,
                    "field_ids": best_match.field_ids_json or [],  # JSONB -> already a native list
                }

            return None
        finally:
            db.close()

    def _compute_similarity(self, doc_fp: dict, tmpl_fp: dict) -> float:
        """Compute Jaccard-like similarity between fingerprints."""
        scores = []

        doc_headings = [h.lower() for h in doc_fp.get("heading_sequence", [])]
        tmpl_headings = [h.lower() for h in tmpl_fp.get("heading_sequence", [])]
        if doc_headings and tmpl_headings:
            common = sum(1 for h in doc_headings if any(self._heading_match(h, th) for th in tmpl_headings))
            scores.append(common / max(len(doc_headings), len(tmpl_headings)))

        doc_tables = set(doc_fp.get("table_signatures", []))
        tmpl_tables = set(tmpl_fp.get("table_signatures", []))
        if doc_tables and tmpl_tables:
            intersection = len(doc_tables & tmpl_tables)
            scores.append(intersection / max(len(doc_tables), len(tmpl_tables)))

        doc_bank = doc_fp.get("bank_name")
        tmpl_bank = tmpl_fp.get("bank_name")
        if doc_bank and tmpl_bank:
            scores.append(1.0 if doc_bank.lower() == tmpl_bank.lower() else 0.0)

        if not scores:
            return 0.0

        weights = [0.5, 0.3, 0.2]
        weighted = sum(s * w for s, w in zip(scores, weights[:len(scores)]))
        return weighted

    def _heading_match(self, h1: str, h2: str) -> bool:
        h1_clean = re.sub(r"[^\w\s]", "", h1.lower()).strip()
        h2_clean = re.sub(r"[^\w\s]", "", h2.lower()).strip()
        return h1_clean == h2_clean or h1_clean in h2_clean or h2_clean in h1_clean


class RuleExecutor:
    """Executes text-based extraction rules against document blocks."""

    def __init__(self, parsed_doc: dict):
        self.doc = parsed_doc
        self.all_text = self._flatten_text()

    def _flatten_text(self) -> str:
        parts = []
        for page in self.doc.get("pages", []):
            for block in page.get("blocks", []):
                parts.append(block["text"])
        return "\n".join(parts)

    def extract_with_rules(self, field_name: str, rules: List[Dict[str, Any]],
                           aliases: List[str] = None) -> Dict[str, Any]:
        aliases = aliases or []

        for rule in sorted(rules, key=lambda r: r.get("priority", 99)):
            strategy = rule.get("strategy", "")
            pattern = rule.get("pattern", "")

            try:
                if strategy == "section_heading":
                    result = self._exec_section_heading(rule, field_name, aliases)
                elif strategy == "table_row":
                    result = self._exec_table_row(rule, field_name, aliases)
                elif strategy == "label_value_pair":
                    result = self._exec_label_value(rule, field_name, aliases)
                elif strategy == "nearby_keyword":
                    result = self._exec_nearby_keyword(rule, field_name, aliases)
                elif strategy == "sentence_pattern":
                    result = self._exec_sentence_pattern(rule, field_name, aliases)
                else:
                    result = self._exec_regex(pattern, field_name, aliases)

                if result and result.get("value") and result["value"] not in ("", "NOT FOUND"):
                    result["rule_used"] = rule
                    result["status"] = "extracted"
                    return result

            except Exception as e:
                logger.debug("Rule failed for %s: %s", field_name, e)
                continue

        return {
            "field_name": field_name,
            "value": "EXTRACTION_FAILED",
            "status": "failed",
            "evidence": "",
            "rule_used": None,
        }

    def _exec_section_heading(self, rule: dict, field_name: str, aliases: List[str]) -> Optional[dict]:
        target_section = rule.get("section_hint", "")
        pattern = rule.get("pattern", "")
        section_texts = []
        current_section = ""
        for page in self.doc.get("pages", []):
            for block in page.get("blocks", []):
                if block["block_type"] == "heading":
                    current_section = block["text"]
                if self._section_match(current_section, target_section):
                    section_texts.append(block["text"])
        text = "\n".join(section_texts)
        return self._apply_regex(pattern, text, field_name)

    def _exec_table_row(self, rule: dict, field_name: str, aliases: List[str]) -> Optional[dict]:
        for page in self.doc.get("pages", []):
            for table in page.get("tables", []):
                if not table or len(table) < 2:
                    continue
                for row in table[1:]:
                    if not row:
                        continue
                    first_col = str(row[0]).lower() if row[0] else ""
                    if any(alias.lower() in first_col for alias in [field_name] + aliases):
                        value = str(row[1]) if len(row) > 1 and row[1] else ""
                        if value:
                            return {
                                "value": value,
                                "evidence": f"Table row: {' | '.join(str(c) for c in row if c)}",
                            }
        return None

    def _exec_label_value(self, rule: dict, field_name: str, aliases: List[str]) -> Optional[dict]:
        pattern = rule.get("pattern", rf"(?i){re.escape(field_name)}[:\s]+([^\n,;]+)")
        return self._apply_regex(pattern, self.all_text, field_name)

    def _exec_nearby_keyword(self, rule: dict, field_name: str, aliases: List[str]) -> Optional[dict]:
        pattern = rule.get("pattern", "")
        match = re.search(pattern, self.all_text, re.DOTALL)
        if match:
            snippet = match.group(0)[:400]
            value = self._extract_value_from_snippet(snippet, field_name, aliases)
            return {"value": value, "evidence": snippet}
        return None

    def _exec_sentence_pattern(self, rule: dict, field_name: str, aliases: List[str]) -> Optional[dict]:
        pattern = rule.get("pattern", "")
        return self._apply_regex(pattern, self.all_text, field_name)

    def _exec_regex(self, pattern: str, field_name: str, aliases: List[str]) -> Optional[dict]:
        return self._apply_regex(pattern, self.all_text, field_name)

    def _apply_regex(self, pattern: str, text: str, field_name: str) -> Optional[dict]:
        if not pattern:
            return None
        try:
            match = re.search(pattern, text, re.IGNORECASE | re.DOTALL)
            if match:
                value = match.group(1).strip() if match.lastindex else match.group(0).strip()
                value = re.sub(r"^[:\s]+", "", value)
                if len(value) > 300:
                    value = value[:300]
                return {"value": value, "evidence": match.group(0)[:400]}
        except re.error as e:
            logger.warning("Invalid regex pattern '%s': %s", pattern, e)
        return None

    def _section_match(self, current: str, target: str) -> bool:
        if not target:
            return True
        c = re.sub(r"[^\w\s]", "", current.lower())
        t = re.sub(r"[^\w\s]", "", target.lower())
        return t in c or c in t

    def _extract_value_from_snippet(self, snippet: str, field_name: str, aliases: List[str]) -> str:
        for alias in [field_name] + aliases:
            pattern = rf"(?i){re.escape(alias)}[:\s]+([^\n,;]+)"
            m = re.search(pattern, snippet)
            if m:
                return m.group(1).strip()
        m = re.search(r"[\$\u20ac\u00a3]?[\d,]+(?:\.\d+)?[MBKmbk%]?", snippet)
        if m:
            return m.group(0)
        return snippet[:100].strip()
