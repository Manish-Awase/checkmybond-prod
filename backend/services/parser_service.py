# backend/services/parser_service.py
import os
import pdfplumber
from enum import Enum
from typing import List, Dict, Any

class DocType(str, Enum):
    DIGITAL = "digital"   # clean text layer
    SCANNED = "scanned"   # image-only, needs OCR
    MIXED   = "mixed"     # some digital, some scanned pages

class ParserService:
    """
    Implements the Document Processing Pipeline from the mind map.

    Two strategies:
    1. Rule-based (deterministic): pdfplumber for digital PDFs
       → Use when: consistent, template-based documents
    2. ML-based (probabilistic): AWS Textract for scanned docs
       → Use when: scanned, variable-layout, handwritten

    This is exactly the decision tree from the Codebasics slide:
    "Rule-based (pdfplumber, PyMuPDF) vs ML-based (layoutparser, Textract)"
    """

    DIGITAL_TEXT_THRESHOLD = 100  # chars on first page to classify as digital

    def detect_doc_type(self, pdf_path: str) -> DocType:
        """Route to cheap rule-based or expensive OCR based on text layer."""
        with pdfplumber.open(pdf_path) as pdf:
            first_page_text = pdf.pages[0].extract_text() or ""
        if len(first_page_text.strip()) > self.DIGITAL_TEXT_THRESHOLD:
            return DocType.DIGITAL
        return DocType.SCANNED

    def parse(self, pdf_path: str, doc_hash: str) -> Dict[str, Any]:
        """
        Main entry point. Returns structured parsed document.
        Falls back to AWS Textract for scanned pages — network call,
        requires AWS credentials. Server-side use only. Desktop code
        must use parse_digital_only() instead — see below.
        """
        doc_type = self.detect_doc_type(pdf_path)
        if doc_type == DocType.DIGITAL:
            return self._parse_digital(pdf_path, doc_hash)
        else:
            return self._parse_with_ocr(pdf_path, doc_hash)

    def parse_digital_only(self, pdf_path: str, doc_hash: str) -> Dict[str, Any]:
        """
        Same as parse(), but NEVER falls back to Textract. For use in
        contexts that must stay fully local/offline (the desktop app's
        cli_desktop.py) — a scanned/image-only PDF here raises instead
        of silently making a network call to AWS with no credentials
        (or worse, credentials that shouldn't be on a user's machine).
        """
        doc_type = self.detect_doc_type(pdf_path)
        if doc_type != DocType.DIGITAL:
            raise ValueError(
                "This PDF appears to be scanned/image-only. Local parsing "
                "only supports digital PDFs with a text layer — scanned "
                "documents are not supported in this mode."
            )
        return self._parse_digital(pdf_path, doc_hash)

    def _parse_digital(self, pdf_path: str, doc_hash: str) -> Dict[str, Any]:
        """
        pdfplumber extracts:
        - Text per page (preserving layout)
        - Tables as list of rows (raw, before serialization)
        - Bounding boxes for chunk provenance

        Key insight: extract_text(layout=True) preserves column
        alignment in financial statements — critical for table parsing.
        """
        pages = []
        with pdfplumber.open(pdf_path) as pdf:
            for i, page in enumerate(pdf.pages):
                text   = page.extract_text(layout=True) or ""
                tables = page.extract_tables() or []
                pages.append({
                    "page_num": i + 1,
                    "text":     text,
                    "tables":   tables,
                    "width":    float(page.width),
                    "height":   float(page.height),
                    "doc_hash": doc_hash,
                })
        return {"doc_type": DocType.DIGITAL, "pages": pages, "total_pages": len(pages)}

    def _parse_with_ocr(self, pdf_path: str, doc_hash: str) -> Dict[str, Any]:
        """
        AWS Textract OCR for scanned PDFs.
        Falls back gracefully with empty pages if Textract unavailable.
        Server-side only — never called from parse_digital_only().
        """
        try:
            import boto3
            textract = boto3.client("textract", region_name="us-east-1")
            with open(pdf_path, "rb") as f:
                raw = f.read()
            response = textract.detect_document_text(Document={"Bytes": raw})
            lines = [
                b["Text"]
                for b in response.get("Blocks", [])
                if b["BlockType"] == "LINE"
            ]
            full_text = "\n".join(lines)
            return {
                "doc_type": DocType.SCANNED,
                "pages": [{"page_num": 1, "text": full_text, "tables": [], "doc_hash": doc_hash}],
                "total_pages": 1,
            }
        except Exception as e:
            return {
                "doc_type": DocType.SCANNED,
                "pages": [{"page_num": 1, "text": "", "tables": [], "doc_hash": doc_hash}],
                "total_pages": 1,
                "error": str(e),
            }