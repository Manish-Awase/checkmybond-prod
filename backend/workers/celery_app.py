import os
import json
import logging
import shutil
import subprocess
from datetime import datetime, timedelta

from celery import Celery
from celery.signals import worker_init, worker_shutdown, task_prerun, task_postrun
from sqlalchemy import text

from models.db import init_db, SessionLocal, ExtractionJob

logger     = logging.getLogger(__name__)
REDIS_HOST = os.getenv("REDIS_HOST", "localhost")
EXPORT_DIR = os.getenv("EXPORT_DIR", "export")
SLA_HOURS  = int(os.getenv("SLA_HOURS", "24"))

celery_app = Celery(
    "hitl_platform",
    broker  = f"redis://{REDIS_HOST}:6379/1",
    backend = f"redis://{REDIS_HOST}:6379/2",
)

celery_app.conf.update(
    task_acks_late             = True,
    task_reject_on_worker_lost = True,
    worker_prefetch_multiplier = 1,
    task_serializer   = "json",
    accept_content    = ["json"],
    result_serializer = "json",
    timezone   = "UTC",
    enable_utc = True,
    broker_connection_retry_on_startup = True,
    worker_cancel_long_running_tasks_on_connection_loss = True,
)

# ── Worker lifecycle signals ──────────────────────────────────────────────────

@worker_init.connect
def on_worker_start(**kwargs):
    logger.info("Celery worker started — initialising DB...")
    init_db()
    # Metrics wiring is best-effort: import errors here (e.g. this
    # environment's dependencies haven't been installed yet) fall back to
    # the plain logging this worker already had, rather than blocking
    # startup over an observability feature.
    try:
        from monitoring.metrics import start_metrics_server, worker_up
        import monitoring.instrument  # noqa: F401 — registers Celery signal hooks
        start_metrics_server()
        worker_up.labels(worker_name=os.getenv("HOSTNAME", "worker")).set(1)
    except Exception as e:
        logger.warning("Metrics not started: %s", e)
    logger.info("Celery worker ready")


@worker_shutdown.connect
def on_worker_shutdown(**kwargs):
    logger.info("Celery worker shutting down — writing checkpoint...")
    try:
        from monitoring.metrics import worker_up
        worker_up.labels(worker_name=os.getenv("HOSTNAME", "worker")).set(0)
    except Exception:
        pass
    _write_checkpoint()
    logger.info("Checkpoint written. Worker exiting cleanly.")


@task_prerun.connect
def on_task_start(task_id, task, args, kwargs, **extras):
    extraction_id = args[0] if args else None
    if not extraction_id:
        return
    db = SessionLocal()
    try:
        job = db.query(ExtractionJob).filter_by(extraction_id=extraction_id).first()
        if job:
            job.started_at = datetime.utcnow()
            db.commit()
    except Exception as e:
        logger.error("task_prerun DB update failed: %s", e)
    finally:
        db.close()
    logger.info("Task started  | task_id=%s | extraction_id=%s", task_id, extraction_id)


@task_postrun.connect
def on_task_end(task_id, task, args, kwargs, retval, state, **extras):
    logger.info("Task finished | task_id=%s | state=%s", task_id, state)


# ── Checkpoint ────────────────────────────────────────────────────────────────

def _write_checkpoint():
    os.makedirs(EXPORT_DIR, exist_ok=True)
    db = SessionLocal()
    try:
        incomplete = db.query(ExtractionJob).filter(
            ExtractionJob.status.in_(["processing", "pending"])
        ).all()
        checkpoint = {
            "timestamp": datetime.utcnow().isoformat(),
            "incomplete_jobs": [
                {
                    "extraction_id": j.extraction_id,
                    "status":        j.status,
                    "schema_id":     j.schema_id,
                    "pdf_path":      j.pdf_path,
                    "doc_hash":      j.doc_hash,
                    "sla_deadline":  str(j.sla_deadline) if j.sla_deadline else None,
                }
                for j in incomplete
            ],
        }
        fpath = os.path.join(EXPORT_DIR, "checkpoint.json")
        with open(fpath, "w") as f:
            json.dump(checkpoint, f, indent=2)
        logger.info("Checkpoint saved → %s  (%d incomplete jobs)", fpath, len(incomplete))
    except Exception as e:
        logger.error("Checkpoint write failed: %s", e)
    finally:
        db.close()


# ── Retry helper ──────────────────────────────────────────────────────────────

def get_retry_value(retry_result: dict) -> str | None:
    if not retry_result:
        return None
    if retry_result.get("status") == "retry_success":
        return retry_result.get("value")
    if "next" in retry_result:
        return get_retry_value(retry_result["next"])
    return None


def gate_enqueue_ocr_field(extraction_id: str, field, extraction: dict) -> dict:
    from hitl.sla_queue import SLAQueue
    queue = SLAQueue()
    item = {
        "item_id":       f"{extraction_id}:{field.name}",
        "extraction_id": extraction_id,
        "field_name":    field.name,
        "priority":      50,
        "result":        extraction,
    }
    queue.push(item, sla_hours=1)
    return item


# ── Format conversion helpers ─────────────────────────────────────────────────

_IMAGE_EXTS = {".png", ".jpg", ".jpeg", ".tiff", ".tif", ".bmp", ".gif", ".webp"}
_WORD_EXTS  = {".docx", ".doc"}


def _get_original_filename(db, extraction_id: str) -> tuple[str, str]:
    row = db.execute(
        text("SELECT filename FROM documents WHERE extraction_id = :eid"),
        {"eid": extraction_id},
    ).fetchone()
    if row and row[0]:
        fn = row[0]
        return fn, os.path.splitext(fn)[1].lower()
    return "", ""


def _ensure_pdf(input_path: str, export_dir: str) -> tuple[str, str]:
    """
    Convert input file to PDF if it is not already one.
    Returns (path_to_pdf, original_extension).
    """
    ext = os.path.splitext(input_path)[1].lower()
    if ext == ".pdf":
        return input_path, ext

    converted_dir = os.path.join(export_dir, "converted")
    os.makedirs(converted_dir, exist_ok=True)

    base_name = os.path.splitext(os.path.basename(input_path))[0]
    pdf_path = os.path.join(converted_dir, f"{base_name}.pdf")

    if os.path.exists(pdf_path):
        return pdf_path, ext

    if ext in _WORD_EXTS:
        soffice = shutil.which("soffice") or shutil.which("libreoffice")
        if not soffice:
            raise RuntimeError(
                "LibreOffice (soffice) not found in PATH — cannot convert Word documents."
            )
        cmd = [
            soffice, "--headless", "--convert-to", "pdf",
            "--outdir", converted_dir, input_path
        ]
        try:
            subprocess.run(cmd, check=True, capture_output=True, timeout=120)
        except subprocess.CalledProcessError as exc:
            logger.error("LibreOffice conversion failed: %s", exc.stderr.decode())
            raise RuntimeError(f"LibreOffice failed to convert {input_path}")
        lo_out = os.path.join(converted_dir, f"{base_name}.pdf")
        if os.path.exists(lo_out) and lo_out != pdf_path:
            os.rename(lo_out, pdf_path)

    elif ext in _IMAGE_EXTS:
        import fitz
        img_doc = fitz.open(input_path)
        pdfbytes = img_doc.convert_to_pdf()
        pdf_doc = fitz.open("pdf", pdfbytes)
        pdf_doc.save(pdf_path)
        pdf_doc.close()
        img_doc.close()
    else:
        raise ValueError(f"Unsupported file format for OCR pipeline: {ext}")

    return pdf_path, ext


def _pdf_to_image(pdf_path: str, output_path: str, dpi: int = 200):
    """Render first page of a PDF to a PNG image."""
    import fitz
    doc = fitz.open(pdf_path)
    if doc.page_count == 0:
        doc.close()
        raise ValueError("PDF has no pages")
    page = doc[0]
    pix = page.get_pixmap(dpi=dpi)
    pix.save(output_path)
    doc.close()


# ── Main extraction task ──────────────────────────────────────────────────────

@celery_app.task(
    bind        = True,
    max_retries = 3,
    acks_late   = True,
)
def process_document(
    self,
    extraction_id: str,
    pdf_path:      str,
    schema_id:     str,
    doc_hash:      str,
):
    """
    Full async extraction pipeline. Supports PDF, Word and image inputs.
    Non-PDF files are transparently converted to PDF before parsing.
    """
    init_db()
    db  = SessionLocal()
    job = None

    try:
        job = db.query(ExtractionJob).filter_by(extraction_id=extraction_id).first()
        if not job:
            logger.error("Job not found in DB: %s", extraction_id)
            return

        original_filename, orig_ext = _get_original_filename(db, extraction_id)

        job.status     = "processing"
        job.started_at = datetime.utcnow()
        job.error      = None
        db.commit()

        working_pdf_path, _ = _ensure_pdf(pdf_path, EXPORT_DIR)

        from services.schema_service import SchemaServiceImpl
        schema_svc = SchemaServiceImpl(db)
        schema     = schema_svc.get(schema_id)
        field_results = []

        if schema.mode == "ocr":
            from services.ocr_extractor import RegionOCRExtractor
            ocr = RegionOCRExtractor()
            for field in schema.fields:
                extraction = ocr.extract_field(field, working_pdf_path)
                decision = "human_review" if extraction["is_valid"] else "fail"
                queue_item = None
                if decision in ("human_review", "fail"):
                    queue_item = gate_enqueue_ocr_field(
                        extraction_id=extraction_id, field=field, extraction=extraction,
                    )
                field_results.append({
                    "field_name":        field.name,
                    "value":             extraction["raw_value"],
                    "confidence_score":  None,
                    "context_relevance": None,
                    "groundedness":      None,
                    "answer_relevance":  None,
                    "decision":          decision,
                    "item_id":           queue_item.get("item_id") if queue_item else None,
                    "prompt_id":         "none",
                    "retrieved_chunks":  [],
                    "extraction_method": extraction["guard_reason"],
                })
        else:
            from services.parser_service  import ParserService
            from services.normalizer      import Normalizer
            from services.vector_store    import VectorStoreService
            from services.extractor       import FieldExtractor
            from services.evaluator       import RAGEvaluator
            from services.confidence_gate import ConfidenceGate

            parser     = ParserService()
            normalizer = Normalizer()
            vs         = VectorStoreService()
            extractor  = FieldExtractor(db)
            evaluator  = RAGEvaluator()
            gate       = ConfidenceGate(db)

            parsed = parser.parse(working_pdf_path, doc_hash)
            chunks = normalizer.chunk_document(parsed)
            vs.index_chunks(chunks, doc_hash)

            for field in schema.fields:
                extraction = extractor.extract_field(
                    field=field, doc_hash=doc_hash,
                    schema_id=schema_id, schema_service=schema_svc,
                )
                eval_scores = evaluator.evaluate(
                    field_name      = field.name,
                    field_type      = field.field_type.value,
                    extracted_value = extraction["raw_value"],
                    retrieved_texts = extraction["retrieved_texts"],
                    query           = schema_svc.build_query(field),
                )
                gate_result = gate.decide(
                    extraction_id     = extraction_id,
                    extraction_result = extraction,
                    eval_scores       = eval_scores,
                    field             = field,
                    doc_hash          = doc_hash,
                    schema_service    = schema_svc,
                )
                if gate_result["decision"] == "re_extract":
                    retry_val   = get_retry_value(gate_result.get("retry", {}))
                    final_value = retry_val or extraction["raw_value"]
                else:
                    final_value = extraction["raw_value"]

                field_results.append({
                    "field_name":        field.name,
                    "value":             final_value,
                    "confidence_score":  eval_scores["confidence_score"],
                    "context_relevance": eval_scores["context_relevance"],
                    "groundedness":      eval_scores["groundedness"],
                    "answer_relevance":  eval_scores["answer_relevance"],
                    "decision":          gate_result["decision"],
                    "item_id":           gate_result.get("queue_item", {}).get("item_id"),
                    "prompt_id":         extraction["prompt_id"],
                    "retrieved_chunks":  extraction["retrieved_chunk_ids"],
                })

        needs_review = any(
            r["decision"] in ("human_review", "fail") for r in field_results
        )
        final_status = "pending" if needs_review else "complete"
        result_json_str = json.dumps(field_results)

        from workers.pending_cache import save_pending_result, delete_pending
        save_pending_result(extraction_id, final_status, result_json_str)

        job.status       = final_status
        job.result_json  = result_json_str
        job.completed_at = datetime.utcnow()
        job.error        = None
        db.commit()
        delete_pending(extraction_id)

        logger.info(
            "Extraction complete | id=%s | status=%s | fields=%d | orig=%s",
            extraction_id, job.status, len(field_results), original_filename or pdf_path,
        )

    except Exception as exc:
        logger.error(
            "Extraction failed | id=%s | attempt=%d/%d | err=%s",
            extraction_id, self.request.retries + 1, self.max_retries, exc,
        )
        if job:
            if self.request.retries >= self.max_retries:
                job.status = "failed"
                job.error  = str(exc)
                logger.error("All retries exhausted — marked as failed: %s", extraction_id)
            else:
                job.status = "processing"
            db.commit()
        raise self.retry(exc=exc, countdown=60)
    finally:
        db.close()


@celery_app.task(
    name="workers.celery_app.process_single_ocr_document",
    bind=True,
    max_retries=3,
    acks_late=True,
)
def process_single_ocr_document(
    self,
    extraction_id: str,
    pdf_path: str,
    schema_id: str,
    doc_hash: str,
    regions_json: str,
    serial_number: int = 1,
):
    """
    Direct single-PDF processing with pre-supplied corrections.
    Supports PDF, Word and image inputs. Image originals get a corrected PNG too.
    """
    import os
    import json
    from datetime import datetime

    init_db()
    db = SessionLocal()
    job = None

    try:
        job = db.query(ExtractionJob).filter_by(extraction_id=extraction_id).first()
        if not job:
            logger.error("Job not found in DB: %s", extraction_id)
            return

        original_filename, orig_ext = _get_original_filename(db, extraction_id)

        job.status = "processing"
        job.started_at = datetime.utcnow()
        job.error = None
        db.commit()

        working_pdf_path, _ = _ensure_pdf(pdf_path, EXPORT_DIR)

        from services.ocr_extractor import RegionOCRExtractor
        import fitz

        ocr = RegionOCRExtractor()
        regions = json.loads(regions_json)

        EXPORT_DIR = os.getenv("EXPORT_DIR", "export")
        CORRECTED_ROOT = os.path.join(EXPORT_DIR, "corrected_pdf")
        PDF_DIR = os.path.join(CORRECTED_ROOT, "pdf")
        CORR_DIR = os.path.join(CORRECTED_ROOT, "corrections")
        XLSX_PATH = os.path.join(CORR_DIR, "corrections.xlsx")
        os.makedirs(PDF_DIR, exist_ok=True)
        os.makedirs(CORR_DIR, exist_ok=True)

        if not os.path.exists(working_pdf_path):
            raise FileNotFoundError(f"Source PDF not found: {working_pdf_path}")

        doc = fitz.open(working_pdf_path)
        field_results = []

        class FieldProxy:
            class _DefaultFieldType:
                value = "text"
            def __init__(self, name, region, field_type=None):
                self.name = name
                self.region = region
                self.field_type = field_type or self._DefaultFieldType()

        for idx, reg in enumerate(regions):
            field_name = reg.get("field_name", f"field_{idx}")
            page_num = int(reg.get("page", 1)) - 1
            x = reg.get("x", 0)
            y = reg.get("y", 0)
            w = reg.get("w", 0)
            h = reg.get("h", 0)
            correction = reg.get("correction_value", "")

            field_proxy = FieldProxy(field_name, reg)
            extraction = ocr.extract_field(field_proxy, working_pdf_path)

            if correction and correction.strip():
                if 0 <= page_num < doc.page_count:
                    page = doc[page_num]
                    text = correction.strip()
                    rect = fitz.Rect(x, y, x + w, y + h)
                    page.draw_rect(rect, color=(1, 1, 1), fill=(1, 1, 1))

                    fixed_size = reg.get("fixed_text_size", False)
                    if fixed_size:
                        font_size = min(h * 0.75, 14)
                        font_size = max(font_size, 6)
                    else:
                        text_len = max(len(text), 1)
                        font_size = min(
                            (w - 6) / (text_len * 0.6),
                            h * 0.75,
                            14,
                        )
                        font_size = max(font_size, 6)

                    rc = page.insert_textbox(
                        rect, text, fontsize=font_size, fontname="helv",
                        color=(0, 0, 0), align=fitz.TEXT_ALIGN_LEFT, overlay=True,
                    )

                    if rc < 0:
                        if fixed_size:
                            page.insert_text(
                                rect.tl + fitz.Point(2, min(h * 0.7, 12)),
                                text, fontsize=font_size, fontname="helv",
                                color=(0, 0, 0), overlay=True,
                            )
                            logger.warning(
                                "Text overflow (fixed size) | field=%s | text='%s' | box=%.0fx%.0f | font=%.1f",
                                field_name, text, w, h, font_size,
                            )
                        else:
                            page.insert_text(
                                rect.tl + fitz.Point(2, min(h * 0.7, 12)),
                                text, fontsize=6, fontname="helv",
                                color=(0, 0, 0), overlay=True,
                            )
                            logger.warning(
                                "Text forced | field=%s | text='%s' | box=%.0fx%.0f",
                                field_name, text, w, h,
                            )

                    logger.info(
                        "Applied correction | field=%s | text='%s' | box=%.0fx%.0f@(%.0f,%.0f) | font=%.1f | rc=%d",
                        field_name, text, w, h, x, y, font_size, rc,
                    )

                    _update_corrections_xlsx(
                        XLSX_PATH,
                        serial_number=serial_number,
                        pdf_name=os.path.basename(original_filename or pdf_path),
                        field_name=field_name,
                        original_value=extraction.get("raw_value", ""),
                        corrected_value=correction,
                    )

            field_results.append({
                "field_name": field_name,
                "value": extraction.get("raw_value", ""),
                "corrected_value": correction,
                "extraction_method": extraction.get("guard_reason", "ocr"),
            })

        base_name = os.path.splitext(os.path.basename(original_filename or pdf_path))[0]
        out_pdf_name = f"{serial_number}_{base_name}.pdf"
        out_pdf_path = os.path.join(PDF_DIR, out_pdf_name)
        doc.save(out_pdf_path, garbage=4, deflate=True)
        doc.close()

        result_payload = {
            "status": "complete",
            "corrected_pdf": out_pdf_path,
            "fields_processed": len(field_results),
            "fields": field_results,
        }

        # For image originals, also render a corrected PNG
        if orig_ext in _IMAGE_EXTS:
            out_png_name = f"{serial_number}_{base_name}.png"
            out_png_path = os.path.join(PDF_DIR, out_png_name)
            _pdf_to_image(out_pdf_path, out_png_path)
            result_payload["corrected_image"] = out_png_path

        result_json_str = json.dumps(result_payload)

        from workers.pending_cache import save_pending_result, delete_pending
        save_pending_result(extraction_id, "complete", result_json_str)

        job.status = "complete"
        job.result_json = result_json_str
        job.completed_at = datetime.utcnow()
        job.error = None
        db.commit()
        delete_pending(extraction_id)

        logger.info(
            "Single OCR direct complete | id=%s | serial=%d | fields=%d | out=%s | orig=%s",
            extraction_id, serial_number, len(field_results), out_pdf_path,
            original_filename or pdf_path,
        )
        return result_payload

    except Exception as exc:
        logger.error("Single OCR direct failed | id=%s | err=%s", extraction_id, exc)
        if job:
            if self.request.retries >= self.max_retries:
                job.status = "failed"
                job.error = str(exc)
                db.commit()
                logger.error("All retries exhausted — marked as failed: %s", extraction_id)
            else:
                job.status = "processing"
                db.commit()
        raise self.retry(exc=exc, countdown=60)
    finally:
        db.close()


# ── Correction PDF export task ────────────────────────────────────────────────

@celery_app.task(
    name        = "workers.celery_app.apply_correction_to_pdf",
    bind        = True,
    max_retries = 3,
    acks_late   = True,
)
def apply_correction_to_pdf(
    self,
    correction_id:   str,
    extraction_id:   str,
    pdf_path:        str,
    field_name:      str,
    original_value:  str,
    corrected_value: str,
    serial_number:   int,
    original_filename: str,
):
    """
    Celery task — applies a human correction to a document.
    Supports PDF, Word and image originals. Non-PDF inputs are converted
    to PDF first, corrections applied, and image originals get a PNG render too.
    """
    import os
    from datetime import datetime

    EXPORT_DIR     = os.getenv("EXPORT_DIR", "export")
    CORRECTED_ROOT = os.path.join(EXPORT_DIR, "corrected_pdf")
    PDF_DIR        = os.path.join(CORRECTED_ROOT, "pdf")
    CORR_DIR       = os.path.join(CORRECTED_ROOT, "corrections")
    XLSX_PATH      = os.path.join(CORR_DIR, "corrections.xlsx")

    os.makedirs(PDF_DIR,  exist_ok=True)
    os.makedirs(CORR_DIR, exist_ok=True)

    try:
        import fitz
        import pdfplumber
        import openpyxl
        from openpyxl.styles import Font, PatternFill, Alignment, Border, Side

        working_pdf_path, orig_ext = _ensure_pdf(pdf_path, EXPORT_DIR)

        if not os.path.exists(working_pdf_path):
            raise FileNotFoundError(f"Source PDF not found: {working_pdf_path}")

        # 1. Locate original text bounding box via pdfplumber
        target_rect  = None
        target_page  = 0
        font_size    = 11

        with pdfplumber.open(working_pdf_path) as plumb:
            for page_num, page in enumerate(plumb.pages):
                words = page.extract_words(keep_blank_chars=True)
                text_on_page = " ".join(w["text"] for w in words)
                if original_value.strip() in text_on_page:
                    orig_words = original_value.strip().split()
                    i = 0
                    while i <= len(words) - len(orig_words):
                        span = words[i : i + len(orig_words)]
                        span_text = " ".join(w["text"] for w in span)
                        if span_text == original_value.strip():
                            x0 = min(w["x0"]   for w in span)
                            y0 = min(w["top"]   for w in span)
                            x1 = max(w["x1"]    for w in span)
                            y1 = max(w["bottom"] for w in span)
                            target_rect = (x0, y0, x1, y1)
                            target_page = page_num
                            font_size = round(y1 - y0)
                            break
                        i += 1
                    if target_rect:
                        break

        if not target_rect:
            logger.warning(
                "apply_correction_to_pdf: could not locate '%s' in %s — using fallback annotation",
                original_value, pdf_path,
            )
            target_rect = (36, 36, 400, 60)
            target_page = 0
            font_size   = 11

        # 2. Apply correction using PyMuPDF
        doc = fitz.open(working_pdf_path)
        page = doc[target_page]
        page_height = page.rect.height
        x0, y0_top, x1, y1_top = target_rect
        y0 = page_height - y1_top
        y1 = page_height - y0_top
        rect = fitz.Rect(x0, y0, x1, y1)

        page.draw_rect(rect, color=(1, 1, 1), fill=(1, 1, 1))
        page.insert_textbox(
            rect, corrected_value, fontsize=max(font_size, 6),
            fontname="helv", color=(0, 0, 0), align=fitz.TEXT_ALIGN_LEFT,
        )

        # 3. Save corrected PDF
        base_name = os.path.splitext(os.path.basename(original_filename or pdf_path))[0]
        out_pdf_name = f"{serial_number}_{base_name}.pdf"
        out_pdf_path = os.path.join(PDF_DIR, out_pdf_name)

        if os.path.exists(out_pdf_path):
            doc.close()
            doc = fitz.open(out_pdf_path)
            page = doc[target_page]
            page.draw_rect(rect, color=(1,1,1), fill=(1,1,1))
            page.insert_textbox(rect, corrected_value, fontsize=max(font_size,6),
                fontname="helv", color=(0,0,0), align=fitz.TEXT_ALIGN_LEFT)

        doc.save(out_pdf_path, garbage=4, deflate=True)
        doc.close()
        logger.info("Saved corrected PDF: %s", out_pdf_path)

        # For image originals, also render corrected PNG
        out_image_path = None
        if orig_ext in _IMAGE_EXTS:
            out_png_name = f"{serial_number}_{base_name}.png"
            out_image_path = os.path.join(PDF_DIR, out_png_name)
            _pdf_to_image(out_pdf_path, out_image_path)

        # 4. Update Excel corrections log
        _update_corrections_xlsx(
            XLSX_PATH, serial_number,
            os.path.basename(original_filename or pdf_path),
            field_name, original_value, corrected_value,
        )

        result = {
            "status":        "ok",
            "corrected_pdf": out_pdf_path,
            "xlsx":          XLSX_PATH,
        }
        if out_image_path:
            result["corrected_image"] = out_image_path
        return result

    except Exception as exc:
        logger.error("apply_correction_to_pdf failed for %s: %s", pdf_path, exc)
        raise self.retry(exc=exc, countdown=30)


def _update_corrections_xlsx(
    xlsx_path:       str,
    serial_number:   int,
    pdf_name:        str,
    field_name:      str,
    original_value:  str,
    corrected_value: str,
):
    """Append one row to corrections.xlsx. Creates with headers if missing."""
    import openpyxl
    from openpyxl.styles import Font, PatternFill, Alignment, Border, Side

    HEADERS = ["Serial No.", "PDF Name", "Field Name", "Original Text", "Correction"]

    if os.path.exists(xlsx_path):
        wb = openpyxl.load_workbook(xlsx_path)
        ws = wb.active
    else:
        wb = openpyxl.Workbook()
        ws = wb.active
        ws.title = "Corrections"
        header_fill   = PatternFill("solid", fgColor="1E40AF")
        header_font   = Font(bold=True, color="FFFFFF", size=11)
        header_align  = Alignment(horizontal="center", vertical="center", wrap_text=True)
        thin_border   = Border(
            left=Side(style="thin"), right=Side(style="thin"),
            top=Side(style="thin"), bottom=Side(style="thin"),
        )
        for col_num, header in enumerate(HEADERS, start=1):
            cell = ws.cell(row=1, column=col_num, value=header)
            cell.fill      = header_fill
            cell.font      = header_font
            cell.alignment = header_align
            cell.border    = thin_border
        col_widths = [12, 35, 25, 45, 45]
        for i, w in enumerate(col_widths, start=1):
            ws.column_dimensions[openpyxl.utils.get_column_letter(i)].width = w
        ws.row_dimensions[1].height = 30

    next_row = ws.max_row + 1
    data = [serial_number, pdf_name, field_name, original_value, corrected_value]
    row_align = Alignment(vertical="top", wrap_text=True)
    for col_num, value in enumerate(data, start=1):
        cell = ws.cell(row=next_row, column=col_num, value=value)
        cell.alignment = row_align
    if next_row % 2 == 0:
        row_fill = PatternFill("solid", fgColor="EFF6FF")
        for col_num in range(1, len(HEADERS) + 1):
            ws.cell(row=next_row, column=col_num).fill = row_fill
    wb.save(xlsx_path)


# ── Preflight fingerprinting (BondDoc AI integration) ───────────────────────
#
# NOTE: this task will fail at import time until services/bond_extractor.py
# and services/template_matcher.py exist — neither is part of this
# codebase yet (see PreflightFingerprinter's own imports). This wiring is
# ready for them; it does not fabricate their logic.

@celery_app.task(
    name="workers.celery_app.preflight_fingerprint",
    bind=True,
    max_retries=1,
    acks_late=True,
)
def preflight_fingerprint(self, preflight_id: str, pdf_path: str, doc_type: str, org_id: str):
    """
    Parse a single doc, fingerprint it, match against this org's templates.
    Result is cached in Redis DB 0 for the Go server to poll (the Go side's
    SetPreflightResult/GetPreflightResult in internal/queue/preflight_enqueue.go
    now reads the same DB — see that file's header comment for why DB 0
    specifically, not the Celery broker DB).
    """
    from services.template_matcher_preflight import PreflightFingerprinter
    import redis
    import os
    import json

    logger.info("Preflight start | id=%s path=%s type=%s org=%s", preflight_id, pdf_path, doc_type, org_id)

    try:
        fingerprinter = PreflightFingerprinter()
        result = fingerprinter.preflight(pdf_path, doc_type, org_id)

        redis_host = os.getenv("REDIS_HOST", "localhost")
        r = redis.from_url(f"redis://{redis_host}:6379/0", decode_responses=True)
        key = f"preflight:{preflight_id}"
        r.setex(key, 600, json.dumps(result))  # TTL 10 min

        if result.get("can_auto_extract") and result.get("template_id"):
            logger.info("Preflight matched | template=%s score=%s — ready for auto-extract",
                        result["template_id"], result.get("score"))

        logger.info("Preflight complete | id=%s status=%s", preflight_id, result.get("status"))
        return result

    except Exception as exc:
        logger.error("Preflight failed | id=%s: %s", preflight_id, exc)
        raise self.retry(exc=exc, countdown=10)


def _record_template_result(template_id: str, success: bool):
    """
    Update drift counters on template_definitions. Call this after each
    rule execution in whatever extraction task ends up applying a
    template's cached rules to a real document — that extraction task
    itself isn't part of this upload (see services/bond_extractor.py gap).
    """
    from models.db import SessionLocal, TemplateDefinition
    db = SessionLocal()
    try:
        # NOTE: filter by template_id, not id -- TemplateDefinition's
        # primary key column is named template_id (see models/db.py).
        tmpl = db.query(TemplateDefinition).filter_by(template_id=template_id).first()
        if not tmpl:
            return
        if success:
            tmpl.success_count += 1
        else:
            tmpl.fail_count += 1
        tmpl.updated_at = datetime.utcnow()
        db.commit()
    except Exception as e:
        logger.warning("Drift stat update failed: %s", e)
    finally:
        db.close()


# ── Bond document tasks (BondDoc AI integration) ────────────────────────────
#
# process_bond_documents runs its OWN per-document template match + rule
# execution (self-sufficient), rather than depending on the separate
# preflight_fingerprint task having already run. This matches how
# documents_bond_additions.go's handleBondUpload actually calls it --
# unconditionally, right after upload, per document -- not gated on a
# prior /check-match call. Session status becomes 'extracted' once every
# uploaded document has been processed, which is what
# routers/reconcile.go's handleGetReconciliation polls for before
# triggering run_reconciliation.

def _resolve_session_org_id(db, session) -> str:
    from models.db import User
    user = db.query(User).filter_by(id=session.user_id).first()
    if not user or not user.org_id:
        raise ValueError(f"user {session.user_id} has no organization assigned")
    return user.org_id


def _load_selected_fields(db, session):
    """Returns {field_library_id: (name, field_type, aliases)} for the
    fields this session's user selected."""
    from models.db import FieldLibrary
    ids = session.selected_field_ids or []
    if not ids:
        return {}
    fields = db.query(FieldLibrary).filter(FieldLibrary.id.in_(ids)).all()
    return {f.id: (f.name, f.field_type, f.aliases or []) for f in fields}


def _extract_doc_fields(parsed_doc: dict, template, field_defs: dict) -> dict:
    """
    Runs RuleExecutor against one parsed document using a matched
    template's rules_json. field_defs is {field_id: (name, type, aliases)}
    from _load_selected_fields. Returns {field_name: {value, confidence,
    evidence, rule_used, edited: False}} -- the shape reconcile.go's
    handleEditExtraction and reconciliation_engine.py both expect.
    """
    from services.template_matcher import RuleExecutor

    executor = RuleExecutor(parsed_doc)
    rules_by_field = (template.rules_json or {}) if template else {}
    results = {}

    for field_id, (name, field_type, aliases) in field_defs.items():
        rules = rules_by_field.get(name, [])
        if not rules:
            results[name] = {
                "value": "EXTRACTION_FAILED",
                "confidence": 0.0,
                "evidence": "",
                "rule_used": None,
                "status": "no_template_rules",
                "edited": False,
            }
            continue
        outcome = executor.extract_with_rules(name, rules, aliases)
        results[name] = {
            "value": outcome.get("value"),
            "confidence": 1.0 if outcome.get("status") == "extracted" else 0.0,
            "evidence": outcome.get("evidence", ""),
            "rule_used": outcome.get("rule_used"),
            "status": outcome.get("status"),
            "edited": False,
        }
    return results


@celery_app.task(
    name="workers.celery_app.process_bond_documents",
    bind=True,
    max_retries=2,
    acks_late=True,
)
def process_bond_documents(self, session_id: str, doc_type: str, pdf_path: str, doc_hash: str):
    from models.db import SessionLocal, BondSession
    from services.bond_extractor import BondDocumentParser
    from services.template_matcher import TemplateMatcher

    db = SessionLocal()
    try:
        session = db.query(BondSession).filter_by(session_id=session_id).first()
        if not session:
            logger.error("process_bond_documents: session not found: %s", session_id)
            return

        org_id = _resolve_session_org_id(db, session)

        parser = BondDocumentParser()
        parsed = parser.parse(pdf_path, doc_hash)

        # Update this doc's entry in session.documents with its parsed_json
        # -- reassign the whole list so SQLAlchemy detects the JSONB change.
        docs = list(session.documents or [])
        for d in docs:
            if d.get("doc_type") == doc_type and d.get("pdf_path") == pdf_path:
                d["parsed_json"] = parsed
        session.documents = docs

        matcher = TemplateMatcher()
        match = matcher.find_best_template(parsed, org_id)

        field_defs = _load_selected_fields(db, session)
        template = None
        if match:
            from models.db import TemplateDefinition
            template = db.query(TemplateDefinition).filter_by(template_id=match["template_id"]).first()

        doc_results = _extract_doc_fields(parsed, template, field_defs)

        extraction_results = dict(session.extraction_results or {})
        extraction_results[doc_type] = doc_results
        session.extraction_results = extraction_results

        if template:
            session.template_id = template.template_id

        # 'extracted' once every uploaded document has an entry in
        # extraction_results -- documents list is fixed at upload time
        # (documents_bond_additions.go writes it once), so this is a
        # simple len() comparison.
        if len(extraction_results) >= len(docs):
            session.status = "extracted"
        db.commit()

        logger.info("process_bond_documents complete | session=%s doc_type=%s template=%s",
                    session_id, doc_type, template.template_id if template else None)

    except Exception as exc:
        logger.error("process_bond_documents failed | session=%s doc_type=%s: %s",
                     session_id, doc_type, exc)
        raise self.retry(exc=exc, countdown=15)
    finally:
        db.close()


@celery_app.task(
    name="workers.celery_app.generate_template_rules",
    bind=True,
    max_retries=1,
    acks_late=True,
)
def generate_template_rules(self, session_id: str, org_id: str):
    """
    org_id is passed explicitly by routers/templates.go's
    handleGenerateRules (resolved server-side there) -- not re-derived
    here, consistent with template_learning.py no longer accepting a
    default org_id.
    """
    from services.template_learning import TemplateLearningEngine

    engine = TemplateLearningEngine()
    result = engine.generate_rules_from_session(session_id, org_id)

    if result.get("status") == "success":
        # Re-run extraction for every document in this session now that a
        # template exists -- otherwise onTrainingComplete's pollExtraction
        # (BondUploadPage.tsx) has nothing to poll toward "extracted".
        from models.db import SessionLocal, BondSession, TemplateDefinition
        db = SessionLocal()
        try:
            session = db.query(BondSession).filter_by(session_id=session_id).first()
            template = db.query(TemplateDefinition).filter_by(template_id=result["template_id"]).first()
            if session and template:
                field_defs = _load_selected_fields(db, session)
                extraction_results = {}
                for d in (session.documents or []):
                    parsed = d.get("parsed_json")
                    if not parsed:
                        continue
                    extraction_results[d["doc_type"]] = _extract_doc_fields(parsed, template, field_defs)
                session.extraction_results = extraction_results
                session.status = "extracted" if len(extraction_results) >= len(session.documents or []) else "learned"
                db.commit()
        finally:
            db.close()

    return result


@celery_app.task(
    name="workers.celery_app.retrain_template",
    bind=True,
    max_retries=1,
    acks_late=True,
)
def retrain_template(self, template_id: str, annotations_json: str):
    import json as _json
    from services.template_learning import TemplateLearningEngine

    annotations = _json.loads(annotations_json)
    engine = TemplateLearningEngine()
    return engine.retrain_template(template_id, annotations)


@celery_app.task(
    name="workers.celery_app.run_reconciliation",
    bind=True,
    max_retries=1,
    acks_late=True,
)
def run_reconciliation_task(self, session_id: str):
    """
    Named run_reconciliation_task (not run_reconciliation) to avoid
    shadowing services.reconciliation_engine.run_reconciliation, which
    this simply calls -- the Celery task NAME (what Go enqueues by
    string) is still "workers.celery_app.run_reconciliation" via the
    `name=` kwarg above, matching what reconcile.go expects.
    """
    from services.reconciliation_engine import run_reconciliation
    return run_reconciliation(session_id)
