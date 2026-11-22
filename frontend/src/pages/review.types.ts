// frontend/src/pages/review.types.ts

export type FailureType =
  | "parser_failure"
  | "retrieval_failure"
  | "reasoning_failure"
  | "schema_failure"
  | "validation_failure"

export const FAILURE_DESCRIPTIONS: Record<FailureType, string> = {
  parser_failure:     "PDF couldn't be parsed — empty text, OCR issue",
  retrieval_failure:  "Wrong chunks returned — embedding mismatch",
  reasoning_failure:  "Correct chunks, but wrong value extracted",
  schema_failure:     "Field name didn't match document terminology",
  validation_failure: "Value extracted but wrong format/type",
}

export const FAILURE_FIXES: Record<string, string> = {
  parser_failure:     "Add OCR fallback, check bounding boxes",
  retrieval_failure:  "Domain-tune embeddings, add reranker layer",
  reasoning_failure:  "Improve prompt engineering, use structured output",
  schema_failure:     "Add more aliases to field definition",
  validation_failure: "Add field-type validators and confidence scoring",
}

export function getFailureFix(ft: string): string {
  return FAILURE_FIXES[ft] || "Review system logs"
}

export function confColor(score: number): string {
  if (score > 0.85) return "#16a34a"
  if (score > 0.50) return "#d97706"
  return "#dc2626"
}
