# backend/services/reextractor.py
import re
from services.llm_client import complete, next_provider, current_provider
from services.vector_store import VectorStoreService
from services.schema_service import FieldDefinition

class ReExtractor:
    """
    Implements Re-extraction path from the Confidence Gate.
    The biggest gap from the original 10 projects.

    3 escalating strategies:
    1. expand_context: top-k 3 → 6 (more context might contain the answer)
    2. hyde_query: HyDE — generate hypothetical answer, use as query
       (finds chunks by semantic similarity to expected format)
    3. different_model: switch LLM provider as last resort

    After 3 failed attempts → EXTRACTION_FAILED → escalate to analyst
    with full retry trace attached.
    """

    MAX_ATTEMPTS = 3

    def __init__(self, db):
        self.vs = VectorStoreService()

    def retry(self, field: FieldDefinition, doc_hash: str,
              schema_service, attempt: int = 1) -> dict:
        """Recursive retry with escalating strategies."""

        if attempt > self.MAX_ATTEMPTS:
            return {
                "status":   "EXTRACTION_FAILED",
                "attempts": attempt - 1,
                "escalate": True,
                "message":  "All retry strategies exhausted. Routing to analyst with trace."
            }

        strategy = {1: "expand_context", 2: "hyde_query", 3: "different_model"}[attempt]

        # Get chunks based on strategy
        if strategy == "expand_context":
            query  = schema_service.build_query(field)
            chunks = self.vs.retrieve(query, doc_hash, top_k=6)  # 3→6

        elif strategy == "hyde_query":
            hypo_query = self._generate_hypothetical(field)
            chunks     = self.vs.retrieve(hypo_query, doc_hash, top_k=4)

        else:  # different_model — use same chunks, different model
            query  = schema_service.build_query(field)
            chunks = self.vs.retrieve(query, doc_hash, top_k=3)

        if not chunks:
            return self.retry(field, doc_hash, schema_service, attempt + 1)

        context  = "\n\n---\n\n".join(c["text"] for c in chunks)
        value    = self._extract(field, context, strategy, attempt)

        result = {
            "status":   "retry_success" if value not in ["EXTRACTION_FAILED", "NOT FOUND", ""] else "retry_failed",
            "attempt":  attempt,
            "strategy": strategy,
            "value":    value,
            "chunk_ids":[c["point_id"] for c in chunks],
        }

        if result["status"] == "retry_failed":
            result["next"] = self.retry(field, doc_hash, schema_service, attempt + 1)

        return result

    def _generate_hypothetical(self, field: FieldDefinition) -> str:
        """
        HyDE: Hypothetical Document Embeddings.
        Generate how this field WOULD appear in a real document.
        The hypothetical answer is semantically closer to real document
        text than the bare field name is.

        Example:
        field = "Total Revenue"
        hypothetical = "Total Revenue: $4.5 million for the fiscal year 2024"
        This query retrieves chunks matching the FORMAT, not just the label.
        """
        prompt= f"""
        Write one example sentence showing how '{field.name}' (type: {field.field_type}) would appear in a financial document. Be brief and realistic.

        Example format: "Total Revenue: $4.5M" or "Revenue for fiscal year was $4,500,000"
        Reply with only the example sentence.
            """
        answer = complete(prompt, max_tokens=80)
        return answer.strip()

    def _extract(self, field: FieldDefinition, context: str,
                 strategy: str, attempt: int) -> str:
        prompt = f"""[Retry attempt {attempt}, Strategy: {strategy}]
    Extract the value for: {field.name} (type: {field.field_type})
    Known synonyms: {', '.join(field.aliases) or 'none'}

    Context:
    {context}

    If found: <FINAL_VALUE>value</FINAL_VALUE>
    If truly not present: <FINAL_VALUE>NOT FOUND</FINAL_VALUE>"""

        response = complete(  # ← now genuinely provider-aware, see below
            prompt, max_tokens=150,
            provider=self._provider_for_strategy(strategy),
        )
        match = re.search(r"<FINAL_VALUE>(.*?)</FINAL_VALUE>", response, re.DOTALL)
        return match.group(1).strip() if match else "EXTRACTION_FAILED"

    def _provider_for_strategy(self, strategy: str) -> str:
        """
        expand_context and hyde_query use whatever LLM_PROVIDER is
        configured by default -- only their retrieval strategy differs.
        different_model is the one attempt that's supposed to actually
        be a DIFFERENT model from the first two -- previously this just
        relabeled the same Groq call with different prompt text (see the
        commented-out Anthropic code this replaces), which defeats the
        point of a 3-strategy escalation: if the default model already
        failed twice, asking it a third time rarely helps. This now
        genuinely switches to the next provider in the fallback chain.
        """
        if strategy == "different_model":
            return next_provider(current_provider())
        return None  # use the globally configured LLM_PROVIDER