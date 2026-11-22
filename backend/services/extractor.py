# backend/services/extractor.py
import re
from typing import Dict, Any
from services.llm_client import complete, current_provider, current_model
from services.vector_store import VectorStoreService
from services.prompt_registry import PromptRegistryService
from services.schema_service import FieldDefinition, SchemaServiceImpl

class FieldExtractor:
    def __init__(self, db):
        self.vs      = VectorStoreService()
        self.prompts = PromptRegistryService(db)

    def extract_field(
        self,
        field: FieldDefinition,
        doc_hash: str,
        schema_id: str,
        schema_service: SchemaServiceImpl,
        top_k: int = 3,
    ) -> Dict[str, Any]:
        from utils.timing import timed

        query = schema_service.build_query(field)

        # Step 1: Vector retrieval
        with timed("extraction", "retrieve_chunks", field_name=field.name,
                   meta={"top_k": top_k}):
            chunks = self.vs.retrieve(query, doc_hash, top_k=top_k)

        if not chunks:
            return self._failed_result(field, schema_id, "no_chunks_retrieved", [])

        context = self._format_context(chunks)

        # Step 2: Prompt construction
        with timed("extraction", "build_prompt", field_name=field.name):
            prompt_id, template = self.prompts.get_active("field_extraction")
            prompt = self.prompts.format_prompt(template, field, context)

        # Step 3: LLM call
        with timed("extraction", "llm_call", field_name=field.name,
                   meta={"provider": current_provider(), "model": current_model()}):
            try:
                raw_output = complete(prompt, max_tokens=256)
            except Exception as e:
                return self._failed_result(field, schema_id, f"llm_error: {e}", [])

        # Step 4: Parse + validate
        with timed("extraction", "parse_validate", field_name=field.name):
            extracted_value = self._parse_output(raw_output)
            is_valid, guard_reason = schema_service.validate_value(extracted_value, field)

        return {
            "field_name":           field.name,
            "field_type":           field.field_type.value,
            "raw_value":            extracted_value,
            "is_valid":             is_valid,
            "guard_reason":         guard_reason,
            "retrieved_chunk_ids":  [c["point_id"] for c in chunks],
            "retrieved_texts":      [c["text"] for c in chunks],
            "retrieval_scores":     [c["retrieval_score"] for c in chunks],
            "prompt_id":            prompt_id,
            "schema_id":            schema_id,
            "raw_llm_output":       raw_output,
        }

    def _format_context(self, chunks: list) -> str:
        parts = []
        for i, chunk in enumerate(chunks, 1):
            page    = chunk.get("page_num", "?")
            section = chunk.get("section", "")
            parts.append(f"[Context {i} — Page {page}, Section: {section}]\n{chunk['text']}")
        return "\n\n---\n\n".join(parts)

    def _parse_output(self, raw: str) -> str:
        text  = re.sub(r"<think>.*?</think>", "", raw, flags=re.DOTALL).strip()
        match = re.search(r"<FINAL_VALUE>(.*?)</FINAL_VALUE>", text, re.DOTALL)
        if match:
            value = match.group(1).strip()
            return value if len(value) <= 300 else "EXTRACTION_FAILED"
        lines = [l.strip() for l in text.splitlines() if l.strip()]
        if not lines:
            return "EXTRACTION_FAILED"
        candidate = lines[-1]
        if len(candidate) > 300:
            return "EXTRACTION_FAILED"
        return candidate

    def _failed_result(self, field, schema_id, reason, chunk_ids) -> Dict[str, Any]:
        return {
            "field_name":          field.name,
            "field_type":          field.field_type.value,
            "raw_value":           "EXTRACTION_FAILED",
            "is_valid":            False,
            "guard_reason":        reason,
            "retrieved_chunk_ids": chunk_ids,
            "retrieved_texts":     [],
            "retrieval_scores":    [],
            "prompt_id":           "none",
            "schema_id":           schema_id,
            "raw_llm_output":      "",
        }
