# backend/services/ocr_extractor.py
#
# Direct region-based extraction for mode="ocr" schemas (see
# schema_service.py's SchemaDefinition.mode). This file makes NO network
# calls, imports no LLM SDK, and does not touch the vector store — that's
# the entire point of it existing separately from services/extractor.py,
# which is the RAG/LLM path.
#
# Two-tier strategy per field:
#   1. If the PDF page has a real text layer (digital PDF), crop to the
#      drawn region's bounding box and read text directly via pdfplumber.
#      This is exact — no OCR error risk at all for digital documents.
#   2. If that comes back empty (scanned/image-only page), render just
#      that cropped region to an image and run Tesseract OCR on it —
#      still fully local, still no LLM, no embeddings, no network call.
import logging

import pdfplumber
import pytesseract

logger = logging.getLogger("ocr_extractor")

# Region coordinates come from PDFRegionEditor.tsx as PDF points measured
# from the top-left of the page — pdfplumber's crop()/within_bbox() use the
# same (x0, top, x1, bottom) top-left-origin convention, so no coordinate
# flip is needed here. If that ever changes on the frontend, this is the
# file to revisit.
TESSERACT_IMAGE_DPI = 300  # higher DPI = better OCR accuracy, slower render


class RegionOCRExtractor:
    def extract_field(self, field, pdf_path: str) -> dict:
        """
        field is a schema_service.FieldDefinition with .region set to
        {"page": int, "x": float, "y": float, "w": float, "h": float}
        (1-indexed page, PDF points). Returns a dict shaped to match what
        services/extractor.py's FieldExtractor.extract_field returns, so
        workers/celery_app.py can treat both extraction paths uniformly
        when building field_results.
        """
        region = field.region
        if not region:
            return self._failed_result(field, "no_region_defined")

        try:
            page_num = int(region["page"])
            x, y, w, h = float(region["x"]), float(region["y"]), float(region["w"]), float(region["h"])
        except (KeyError, TypeError, ValueError) as e:
            return self._failed_result(field, f"invalid_region: {e}")

        try:
            with pdfplumber.open(pdf_path) as pdf:
                if page_num < 1 or page_num > len(pdf.pages):
                    return self._failed_result(field, f"page_{page_num}_out_of_range")
                page = pdf.pages[page_num - 1]

                bbox = (x, y, x + w, y + h)
                # Clamp to page bounds — a region drawn slightly outside the
                # page edge (off-by-a-pixel in the editor) shouldn't crash
                # the whole extraction.
                bbox = (
                    max(0, bbox[0]), max(0, bbox[1]),
                    min(page.width, bbox[2]), min(page.height, bbox[3]),
                )
                if bbox[2] <= bbox[0] or bbox[3] <= bbox[1]:
                    return self._failed_result(field, "region_outside_page_bounds")

                cropped = page.crop(bbox)

                # Tier 1 — direct text layer, exact, no OCR guessing at all.
                text = (cropped.extract_text() or "").strip()
                if text:
                    return self._ok_result(field, text, source="text_layer")

                # Tier 2 — no text layer in this region (scanned page or a
                # region that's genuinely an image) — render just this crop
                # to an image and OCR it locally via Tesseract.
                im = cropped.to_image(resolution=TESSERACT_IMAGE_DPI).original
                ocr_text = pytesseract.image_to_string(im).strip()
                if ocr_text:
                    return self._ok_result(field, ocr_text, source="tesseract")

                return self._failed_result(field, "no_text_found_in_region")

        except Exception as e:
            logger.error("OCR extraction failed for field %s: %s", field.name, e)
            return self._failed_result(field, f"ocr_error: {e}")

    def _ok_result(self, field, value: str, source: str) -> dict:
        return {
            "field_name":          field.name,
            "field_type":          field.field_type.value,
            "raw_value":           value,
            "is_valid":            True,
            "guard_reason":        f"ocr_{source}",
            "retrieved_chunk_ids": [],
            "retrieved_texts":     [value],
            "retrieval_scores":    [],
            "prompt_id":           "none",
            "schema_id":           "",
            "raw_llm_output":      "",
        }

    def _failed_result(self, field, reason: str) -> dict:
        return {
            "field_name":          field.name,
            "field_type":          field.field_type.value,
            "raw_value":           "EXTRACTION_FAILED",
            "is_valid":            False,
            "guard_reason":        reason,
            "retrieved_chunk_ids": [],
            "retrieved_texts":     [],
            "retrieval_scores":    [],
            "prompt_id":           "none",
            "schema_id":           "",
            "raw_llm_output":      "",
        }