# backend/services/table_tagger.py
import json
from enum import Enum
from typing import List
from services.llm_client import complete
class TableType(str, Enum):
    FINANCIAL_SUMMARY = "financial_summary"  # P&L, balance sheet totals
    LINE_ITEMS        = "line_items"         # invoice rows, product list
    FOOTNOTES         = "footnotes"          # reference/note tables
    FORM_FIELDS       = "form_fields"        # key-value form data
    COMPARISON        = "comparison"         # side-by-side column data

# From Codebasics "Join Strategies" slide:
# JSON-per-row = highest LLM comprehension, lowest hallucination risk
SERIALIZATION_STRATEGY = {
    TableType.FINANCIAL_SUMMARY: "json_per_row",    # {"Revenue": "$4.5M", "EBITDA": "$1.2M"}
    TableType.LINE_ITEMS:        "markdown_table",  # | Item | Qty | Price |
    TableType.FOOTNOTES:         "prose",            # plain text join
    TableType.FORM_FIELDS:       "key_value",        # Revenue: $4.5M
    TableType.COMPARISON:        "header_aware",     # Revenue: Q1=$2M | Q2=$2.5M
}

class TableTagger:
    def __init__(self):
        pass
    def classify(self, table_rows: list, surrounding_text: str) -> TableType:
        """
        One LLM call to classify table type.
        Sample = first 3 rows to keep prompt short.
        """
        if not table_rows:
            return TableType.FORM_FIELDS

        sample_text = str(table_rows[:3])
        prompt = f"""Classify this table into EXACTLY ONE category from this list:
financial_summary | line_items | footnotes | form_fields | comparison

Table sample (first 3 rows): {sample_text[:400]}
Surrounding document text: {surrounding_text[:200]}

Rules:
- financial_summary: totals, aggregates, P&L, balance sheet
- line_items: repeated rows with same structure (invoice lines, products)
- footnotes: numbered references, small auxiliary tables
- form_fields: key-value pairs like a form
- comparison: side-by-side columns comparing periods/entities

Reply with ONLY the category name, nothing else."""

        tag = complete(prompt, max_tokens=20).strip().lower()
        # Safe fallback if LLM returns unexpected value
        return TableType(tag) if tag in TableType._value2member_map_ else TableType.FINANCIAL_SUMMARY

    def serialize(self, table_rows: List[list], headers: List[str], table_type: TableType) -> str:
        """
        Apply type-specific serialization strategy.

        Key insight (from Codebasics Join Strategies):
        - Raw row join: "ACME Corp, $4.5M" → LOW LLM comprehension (no headers)
        - JSON per row: {"Company":"ACME","Revenue":"$4.5M"} → HIGHEST comprehension
        - Header-aware: "Revenue: $4.5M | EBITDA: $1.2M" → HIGH comprehension
        """
        if not table_rows or not headers:
            return ""

        strategy = SERIALIZATION_STRATEGY[table_type]

        if strategy == "json_per_row":
            # Best for financial tables — LLM always knows which column = which field
            rows_json = []
            for row in table_rows:
                if row and any(cell for cell in row if cell):  # skip empty rows
                    row_dict = {h: (str(v) if v is not None else "") for h, v in zip(headers, row)}
                    rows_json.append(json.dumps(row_dict))
            return "\n".join(rows_json)

        elif strategy == "markdown_table":
            # Good for line items — preserves visual structure
            header_row = "| " + " | ".join(str(h) for h in headers) + " |"
            separator  = "| " + " | ".join("---" for _ in headers) + " |"
            data_rows  = "\n".join(
                "| " + " | ".join(str(c) if c else "" for c in row) + " |"
                for row in table_rows if row
            )
            return f"{header_row}\n{separator}\n{data_rows}"

        elif strategy == "key_value":
            # For form fields — "Revenue: $4.5M"
            lines = []
            for row in table_rows:
                for h, v in zip(headers, row):
                    if v:
                        lines.append(f"{h}: {v}")
            return "\n".join(lines)

        elif strategy == "header_aware":
            # "Revenue: Q1=$2M | Q2=$2.5M"
            return "\n".join(
                " | ".join(f"{h}: {v}" for h, v in zip(headers, row) if v)
                for row in table_rows if row
            )

        else:  # prose — footnotes
            return " ".join(
                str(cell) for row in table_rows for cell in row if cell
            )

    def get_column_headers(self, table_rows: list) -> tuple[list, list]:
        """
        Heuristic: first row is headers if it contains no numeric values.
        Returns (headers, data_rows).
        """
        if not table_rows:
            return [], []
        first_row = table_rows[0] or []
        # Check if first row looks like headers (mostly strings, no numbers)
        is_header = all(
            not str(cell).replace(",","").replace(".","").replace("$","").replace("%","").strip().isdigit()
            for cell in first_row if cell
        )
        if is_header:
            headers = [str(h).strip() if h else f"Col{i}" for i, h in enumerate(first_row)]
            return headers, table_rows[1:]
        else:
            headers = [f"Column{i+1}" for i in range(len(first_row))]
            return headers, table_rows