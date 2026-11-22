# backend/hitl/failure_tagger.py
"""
Failure taxonomy — maps extraction outcomes to structured failure types.
Used by the confidence gate and HITL review UI.
"""
from enum import Enum


class FailureType(str, Enum):
    PARSER_FAILURE     = "parser_failure"      # PDF couldn't be parsed
    RETRIEVAL_FAILURE  = "retrieval_failure"   # Wrong chunks returned
    REASONING_FAILURE  = "reasoning_failure"   # Correct chunks, wrong value
    SCHEMA_FAILURE     = "schema_failure"      # Field name didn't match document
    VALIDATION_FAILURE = "validation_failure"  # Value wrong format/type
    UNKNOWN            = "unknown"


FAILURE_DESCRIPTIONS: dict[str, str] = {
    FailureType.PARSER_FAILURE:     "PDF couldn't be parsed — empty text, OCR issue",
    FailureType.RETRIEVAL_FAILURE:  "Wrong chunks returned — embedding mismatch",
    FailureType.REASONING_FAILURE:  "Correct chunks, but wrong value extracted",
    FailureType.SCHEMA_FAILURE:     "Field name didn't match document terminology",
    FailureType.VALIDATION_FAILURE: "Value extracted but wrong format/type",
    FailureType.UNKNOWN:            "Failure type not yet classified",
}

FAILURE_FIXES: dict[str, str] = {
    FailureType.PARSER_FAILURE:     "Add OCR fallback, check bounding boxes",
    FailureType.RETRIEVAL_FAILURE:  "Domain-tune embeddings, add reranker layer",
    FailureType.REASONING_FAILURE:  "Improve prompt engineering, use structured output",
    FailureType.SCHEMA_FAILURE:     "Add more aliases to field definition",
    FailureType.VALIDATION_FAILURE: "Add field-type validators and confidence scoring",
    FailureType.UNKNOWN:            "Review system logs",
}


def infer_failure_type(
    extraction_result: dict,
    eval_scores: dict,
) -> FailureType:
    """
    Automatically infer failure type from extraction + evaluation signals.
    Used by confidence_gate.py when routing to human review.
    """
    raw_value        = extraction_result.get("raw_value", "")
    is_valid         = extraction_result.get("is_valid", False)
    guard_reason     = extraction_result.get("guard_reason", "")
    context_rel      = eval_scores.get("context_relevance", 1.0)
    groundedness     = eval_scores.get("groundedness", 1.0)
    confidence       = eval_scores.get("confidence_score", 1.0)

    if "no_chunks_retrieved" in guard_reason or "llm_error" in guard_reason:
        return FailureType.PARSER_FAILURE

    if context_rel < 0.3:
        return FailureType.RETRIEVAL_FAILURE

    if not is_valid and raw_value not in ("", "EXTRACTION_FAILED"):
        return FailureType.VALIDATION_FAILURE

    if groundedness < 0.3:
        return FailureType.REASONING_FAILURE

    if "alias" in guard_reason.lower() or "schema" in guard_reason.lower():
        return FailureType.SCHEMA_FAILURE

    return FailureType.UNKNOWN


def get_description(failure_type: str) -> str:
    return FAILURE_DESCRIPTIONS.get(failure_type, FAILURE_DESCRIPTIONS[FailureType.UNKNOWN])


def get_fix(failure_type: str) -> str:
    return FAILURE_FIXES.get(failure_type, FAILURE_FIXES[FailureType.UNKNOWN])
