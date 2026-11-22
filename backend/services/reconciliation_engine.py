"""backend/services/reconciliation_engine.py
Field-value normalization + cross-document comparison for bond sessions.
Called by run_reconciliation Celery task; writes reconciliation_result JSONB.
"""
import json
import re
from datetime import datetime
from decimal import Decimal, InvalidOperation
from typing import Any, Dict, List, Optional

REFERENCE_ORDER = ["prospectus", "term_sheet", "confirmation"]

TOLERANCE = {"percentage": Decimal("0.000000001"), "currency": Decimal("0")}


# ---------- normalization ----------

def normalize_value(value: str, field_type: str, aliases: List[str] = None) -> Any:
    if value is None:
        return None
    v = str(value).strip()
    if not v or v in ("EXTRACTION_FAILED", "NOT FOUND"):
        return None
    ft = (field_type or "text").lower()
    try:
        if ft == "percentage":
            return _norm_percentage(v)
        if ft == "currency":
            return _norm_currency(v)
        if ft == "date":
            return _norm_date(v)
        if ft == "number":
            return _norm_currency(v)
        if ft == "boolean":
            return v.lower() in ("true", "yes", "1")
        return _norm_text(v)
    except Exception:
        return None


def _norm_percentage(v: str) -> Optional[Decimal]:
    frac = re.match(r"^(\d+)\s+(\d+)/(\d+)%?$", v)
    if frac:
        whole = Decimal(frac.group(1))
        frac_part = Decimal(frac.group(2)) / Decimal(frac.group(3))
        return whole + frac_part
    m = re.search(r"(-?[\d.,]+)", v.replace(",", ""))
    if not m:
        return None
    n = Decimal(m.group(1))
    if "percent" not in v and "%" not in v and n < 1:
        n = n * 100          # 0.0525 -> 5.25
    return n


def _norm_currency(v: str) -> Optional[Decimal]:
    s = v.upper().replace(",", "").replace("$", "").replace("\u00a3", "")
    s = s.replace("USD", "").replace("EUR", "").replace("GBP", "").strip()
    mult = Decimal(1)
    if re.search(r"(\d)\s*(MM|M|BN|B)$", s):
        m = re.search(r"(\d)\s*(MM|M|BN|B)$", s)
        mult = Decimal(1000000) if m.group(2) in ("MM", "M") else Decimal(1000000000)
        s = s[:m.start()]
    elif re.search(r"(\d)\s*K$", s):
        m = re.search(r"(\d)\s*K$", s)
        mult = Decimal(1000)
        s = s[:m.start()]
    return Decimal(s) * mult


def _norm_date(v: str) -> Optional[str]:
    from dateutil import parser as dp
    dt = dp.parse(v, dayfirst=_looks_day_first(v))
    return dt.strftime("%Y-%m-%d")


def _looks_day_first(v: str) -> bool:
    m = re.match(r"^(\d{1,2})[/\-.](\d{1,2})[/\-.](\d{2,4})$", v.strip())
    if m and int(m.group(1)) > 12:
        return True
    return False


def _norm_text(v: str) -> str:
    return re.sub(r"\s+", " ", re.sub(r"[^\w\s]", "", v.lower())).strip()


# ---------- comparison ----------

def values_equal(a: Any, b: Any, field_type: str) -> bool:
    if a is None or b is None:
        return False
    if field_type in TOLERANCE:
        try:
            return abs(Decimal(a) - Decimal(b)) <= TOLERANCE[field_type]
        except (InvalidOperation, TypeError):
            return False
    return a == b


def reconcile_field(
    doc_results: Dict[str, dict],
    field_type: str,
    aliases: List[str] = None,
) -> Dict[str, Any]:
    ref_doc = next((d for d in REFERENCE_ORDER if doc_results.get(d, {}).get("value")), None)

    if ref_doc is None:
        return {"status": "missing", "ref_doc_type": None, "cells": {}}

    ref_norm = normalize_value(doc_results[ref_doc]["value"], field_type, aliases)
    if ref_norm is None:
        return {"status": "missing", "ref_doc_type": ref_doc, "cells": {}}

    any_mismatch = False
    cells = {}
    for doc_type in REFERENCE_ORDER:
        cell = doc_results.get(doc_type)
        if not cell or not cell.get("value"):
            continue
        norm = normalize_value(cell["value"], field_type, aliases)
        cells[doc_type] = {
            "raw": cell["value"],
            "normalized": str(norm),
            "matches_ref": values_equal(norm, ref_norm, field_type),
        }
        if not cells[doc_type]["matches_ref"]:
            any_mismatch = True

    others_present = [d for d in REFERENCE_ORDER if d != ref_doc and cells.get(d)]
    status = "mismatch" if (any_mismatch and others_present) else "matched"
    return {"status": status, "ref_doc_type": ref_doc, "cells": cells}


def reconcile_session(extraction_results: Dict[str, Dict[str, dict]],
                      field_types: Dict[str, str]) -> Dict[str, dict]:
    """extraction_results: {doc_type: {field_name: result}}
    Returns per-field reconciliation rows keyed by field name."""
    all_fields = set()
    for doc_map in extraction_results.values():
        all_fields.update(doc_map.keys())

    out = {}
    for fname in sorted(all_fields):
        doc_results = {
            dt: extraction_results[dt][fname]
            for dt in REFERENCE_ORDER if fname in extraction_results.get(dt, {})
        }
        out[fname] = reconcile_field(doc_results, field_types.get(fname, "text"))
    return out


def run_reconciliation(session_id: str) -> Dict[str, Any]:
    """Celery task body. Reads extraction_results, writes reconciliation_result."""
    from models.db import SessionLocal, BondSession, FieldLibrary

    db = SessionLocal()
    try:
        s = db.query(BondSession).filter_by(session_id=session_id).first()
        if not s or not s.extraction_results:
            return {"status": "error", "message": "no extraction results"}

        # extraction_results and reconciliation_result are JSONB columns
        # on BondSession -- SQLAlchemy already deserializes/serializes
        # these automatically. json.loads()/json.dumps() here would
        # double-encode (dumps onto an already-dict target -> stores a
        # JSON string as a scalar, not an object; loads onto an
        # already-decoded dict -> TypeError).
        extractions = s.extraction_results
        type_map = {
            f.name: f.field_type
            for f in db.query(FieldLibrary).filter_by(is_active=True).all()
        }

        reconciliation = reconcile_session(extractions, type_map)

        s.reconciliation_result = {
            "status": "complete",
            "fields": [
                {
                    "field_name": fname,
                    "status": row["status"],
                    "ref_doc_type": row["ref_doc_type"],
                    "cells": row["cells"],
                }
                for fname, row in reconciliation.items()
            ],
        }
        s.status = "reconciled"
        db.commit()
        return {"status": "success", "fields": len(reconciliation)}
    finally:
        db.close()
