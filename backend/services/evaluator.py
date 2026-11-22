# backend/services/evaluator.py
import re
from services.llm_client import complete

class RAGEvaluator:
    WEIGHTS = {
        "context_relevance":  0.35,
        "groundedness":       0.40,
        "answer_relevance":   0.25,
    }

    def __init__(self):
        pass

    def evaluate(self, field_name: str, field_type: str,
                 extracted_value: str, retrieved_texts: list[str],
                 query: str) -> dict:
        from utils.timing import timed

        if extracted_value in ["EXTRACTION_FAILED", ""]:
            return {
                "context_relevance": 0.0,
                "groundedness":      0.0,
                "answer_relevance":  0.0,
                "confidence_score":  0.0,
            }

        # Each of these is a separate LLM call — timed individually
        with timed("evaluation", "context_relevance", field_name=field_name):
            cr = self._context_relevance(query, retrieved_texts)

        with timed("evaluation", "groundedness", field_name=field_name):
            gnd = self._groundedness(extracted_value, retrieved_texts)

        with timed("evaluation", "answer_relevance", field_name=field_name):
            ar = self._answer_relevance(field_name, field_type, extracted_value)

        confidence = (
            self.WEIGHTS["context_relevance"] * cr +
            self.WEIGHTS["groundedness"]      * gnd +
            self.WEIGHTS["answer_relevance"]  * ar
        )

        return {
            "context_relevance": round(cr, 3),
            "groundedness":      round(gnd, 3),
            "answer_relevance":  round(ar, 3),
            "confidence_score":  round(confidence, 3),
        }

    def _context_relevance(self, query: str, chunks: list[str]) -> float:
        context_sample = "\n".join(chunks[:2])[:600]
        prompt = f"""Score 0.0 to 1.0: how relevant is this context to finding the value for '{query}'?

Context: {context_sample}

0.0 = completely unrelated  |  1.0 = directly contains the answer
Return ONLY a decimal number."""
        return self._llm_score(prompt, default=0.5)

    def _groundedness(self, value: str, chunks: list[str]) -> float:
        if value in ["NOT FOUND", "N/A", "EXTRACTION_FAILED"]:
            return 0.0

        context = " ".join(chunks)

        # Fast paths — no LLM needed
        if value.strip() in context:
            return 1.0
        clean_val = re.sub(r"[$,%\s]", "", value)
        clean_ctx = re.sub(r"[$,%\s]", "", context)
        if clean_val and clean_val in clean_ctx:
            return 0.95

        # LLM judge only when fast paths miss
        prompt = f"""Is the extracted value '{value}' supported by any of this context?
Consider numeric equivalents ($4.5M = 4,500,000), abbreviations, and formatting differences.

Context: {context[:800]}

0.0 = not supported (hallucination)  |  1.0 = clearly supported
Return ONLY a decimal number."""
        return self._llm_score(prompt, default=0.3)

    def _answer_relevance(self, field_name: str, field_type: str, value: str) -> float:
        prompt = f"""Score 0.0 to 1.0: does '{value}' make sense as the value for field '{field_name}' (type: {field_type})?

Examples of good answers:
- currency field: $4.5M, 4,500,000, $1.2B
- date field: 2024-12-31, December 31, 2024
- percentage field: 15.3%, 0.153
- ein field: 12-3456789

0.0 = wrong type/format  |  1.0 = correct type and plausible value
Return ONLY a decimal number."""
        return self._llm_score(prompt, default=0.5)

    def _llm_score(self, prompt: str, default: float) -> float:
        try:
            text = complete(prompt, max_tokens=10).strip()
            return max(0.0, min(1.0, float(text)))
        except Exception:
            return default
