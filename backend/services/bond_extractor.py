# backend/services/bond_extractor.py
"""
Layout-aware parser for bond documents.
Wraps parser_service.py and adds section-heading detection
so that 100-page prospectuses can be isolated by section.
"""
import re
import logging
from typing import List, Dict, Any

from services.parser_service import ParserService

logger = logging.getLogger("bond_extractor")

# Bond-document heading patterns (prospectus, term sheet, confirmation)
HEADING_PATTERNS = [
    re.compile(r"^\s*(ITEM|NOTE|SECTION)\s+\d+[.:]\s*(.+)", re.IGNORECASE),
    re.compile(r"^\s*(\d+\.)\s+([A-Z][A-Za-z\s\-]{3,80})\s*$"),
    re.compile(r"^\s*([A-Z][A-Z\s\-&]{4,60})\s*$"),  # ALL CAPS headers
    re.compile(r"^\s*SUMMARY\s+OF\s+(.+)", re.IGNORECASE),
    re.compile(r"^\s*THE\s+[A-Z].{5,80}\s*$"),  # "THE ISSUER", "THE NOTES"
]


class BondDocumentParser:
    """
    Extracts structured text blocks from bond PDFs.
    Each block carries: text, page_num, section, bbox, block_type.
    """

    def __init__(self):
        self.base_parser = ParserService()

    def parse(self, pdf_path: str, doc_hash: str) -> Dict[str, Any]:
        """Main entry: returns parsed doc with section-aware blocks."""
        parsed = self.base_parser.parse(pdf_path, doc_hash)
        enriched_pages = []

        for page in parsed.get("pages", []):
            blocks = self._extract_blocks(page)
            enriched_pages.append({
                "page_num": page["page_num"],
                "width": page.get("width", 0),
                "height": page.get("height", 0),
                "blocks": blocks,
            })

        # Build heading hierarchy across entire document
        heading_hierarchy = self._build_heading_hierarchy(enriched_pages)

        # Detect tables and their signatures
        table_signatures = self._extract_table_signatures(parsed)

        return {
            "doc_hash": doc_hash,
            "doc_type": parsed.get("doc_type", "digital"),
            "total_pages": len(enriched_pages),
            "pages": enriched_pages,
            "heading_hierarchy": heading_hierarchy,
            "table_signatures": table_signatures,
        }

    def _extract_blocks(self, page: dict) -> List[Dict[str, Any]]:
        """Split page text into blocks and tag with section headings."""
        text = page.get("text", "")
        page_num = page["page_num"]
        blocks = []

        # Split on double newlines to get rough blocks
        raw_blocks = [b.strip() for b in text.split("\n\n") if b.strip()]
        current_section = f"page_{page_num}"

        for block_text in raw_blocks:
            # Detect if this block IS a heading
            is_heading, heading_text = self._detect_heading(block_text)
            if is_heading:
                current_section = heading_text
                block_type = "heading"
            else:
                block_type = "prose"

            # Skip trivially short blocks
            if len(block_text) < 10 and not is_heading:
                continue

            blocks.append({
                "text": block_text,
                "page_num": page_num,
                "section": current_section,
                "block_type": block_type,
                "char_count": len(block_text),
            })

        return blocks

    def _detect_heading(self, text: str) -> tuple[bool, str]:
        """Check if text matches any heading pattern."""
        for pattern in HEADING_PATTERNS:
            m = pattern.match(text.strip())
            if m:
                # Return full match or capture group
                heading = m.group(0).strip()
                return True, heading
        return False, ""

    def _build_heading_hierarchy(self, pages: List[dict]) -> List[Dict[str, Any]]:
        """Collect all headings in document order with page numbers."""
        hierarchy = []
        seen = set()
        for page in pages:
            for block in page["blocks"]:
                if block["block_type"] == "heading":
                    key = (block["section"], block["page_num"])
                    if key not in seen:
                        seen.add(key)
                        hierarchy.append({
                            "heading": block["section"],
                            "page_num": block["page_num"],
                            "level": self._heading_level(block["section"]),
                        })
        return hierarchy

    def _heading_level(self, heading: str) -> int:
        """Infer heading level from formatting."""
        h = heading.strip()
        if re.match(r"^(ITEM|NOTE|SECTION)\s+\d+[.:]", h, re.I):
            return 1
        if re.match(r"^\d+\.\s+[A-Z]", h):
            return 2
        if re.match(r"^[A-Z][A-Z\s\-&]{4,60}$", h):
            return 2
        return 3

    def _extract_table_signatures(self, parsed: dict) -> List[Dict[str, Any]]:
        """
        Extract 'signatures' from tables: column headers + first data row.
        These fingerprints help match document types.
        """
        signatures = []
        for page in parsed.get("pages", []):
            for table_idx, table_rows in enumerate(page.get("tables", [])):
                if not table_rows or len(table_rows) < 2:
                    continue
                headers = [str(c).strip() if c else "" for c in table_rows[0]]
                first_row = [str(c).strip() if c else "" for c in table_rows[1]]
                signatures.append({
                    "page_num": page["page_num"],
                    "table_index": table_idx,
                    "headers": headers,
                    "first_row": first_row,
                    "header_fingerprint": "|".join(headers).lower(),
                    "row_count": len(table_rows),
                })
        return signatures


class SectionAwareExtractor:
    """
    Given a parsed bond document and a field definition,
    isolates the most relevant section(s) before extraction.
    """

    def __init__(self, parsed_doc: dict):
        self.doc = parsed_doc
        self.blocks = []
        for page in parsed_doc.get("pages", []):
            self.blocks.extend(page["blocks"])

    def isolate_section(self, field_name: str, aliases: List[str]) -> List[Dict[str, Any]]:
        """
        Return blocks from the most relevant section for this field.
        Uses heading hierarchy to find sections likely to contain the field.
        """
        # Section relevance keywords
        section_keywords = self._section_keywords(field_name, aliases)

        # Score each section by keyword overlap
        section_scores = {}
        for block in self.blocks:
            sec = block["section"]
            if sec not in section_scores:
                section_scores[sec] = 0
            text_lower = block["text"].lower()
            for kw in section_keywords:
                if kw in text_lower:
                    section_scores[sec] += 1
            # Boost headings that contain keywords
            if block["block_type"] == "heading":
                heading_lower = block["text"].lower()
                for kw in section_keywords:
                    if kw in heading_lower:
                        section_scores[sec] += 5

        if not section_scores:
            return self.blocks  # fallback: all blocks

        # Pick top-scoring section(s)
        max_score = max(section_scores.values())
        if max_score == 0:
            return self.blocks

        best_sections = [s for s, sc in section_scores.items() if sc >= max_score * 0.7]
        return [b for b in self.blocks if b["section"] in best_sections]

    def _section_keywords(self, field_name: str, aliases: List[str]) -> List[str]:
        """Generate keywords to find relevant sections."""
        keywords = set()
        for term in [field_name] + aliases:
            keywords.add(term.lower())
            # Add category hints
            if "coupon" in term.lower() or "rate" in term.lower():
                keywords.update(["interest", "coupon", "yield", "pricing"])
            if "maturity" in term.lower():
                keywords.update(["maturity", "redemption", "terms"])
            if "issuer" in term.lower():
                keywords.update(["issuer", "obligor", "borrower", "company"])
            if "principal" in term.lower() or "face" in term.lower():
                keywords.update(["principal", "amount", "denomination", "par"])
            if "rating" in term.lower():
                keywords.update(["rating", "credit", "moody", "fitch", "s&p"])
            if "isin" in term.lower() or "cusip" in term.lower():
                keywords.update(["isin", "cusip", "identifier", "security"])
        return list(keywords)

    def get_evidence_snippet(self, block: dict, max_len: int = 300) -> str:
        """Return a concise evidence snippet from a block."""
        text = block["text"]
        if len(text) <= max_len:
            return text
        # Try to find sentence containing the value
        sentences = re.split(r'(?<=[.!?])\s+', text)
        for s in sentences:
            if len(s) > 20:
                return s[:max_len]
        return text[:max_len]
