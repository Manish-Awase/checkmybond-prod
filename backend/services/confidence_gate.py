# backend/services/confidence_gate.py
from enum import Enum
from hitl.sla_queue import SLAQueue
from services.reextractor import ReExtractor

class GateDecision(str, Enum):
    AUTO_ACCEPT  = "auto_accept"
    HUMAN_REVIEW = "human_review"
    RE_EXTRACT   = "re_extract"
    FAIL = "fail"

class ConfidenceGate:
    """
    Implements the Confidence Gate from the mind map:
    Score > 0.85 → auto-accept → output DB
    Score 0.5–0.85 → SLA queue → human review
    Score < 0.5 → re-extract (strategy rotation)

    This is the most critical decision node in the system.
    Too low threshold = analyst overwhelmed with reviews.
    Too high threshold = hallucinations accepted automatically.
    0.85 is the starting point — tune based on domain risk tolerance.
    """

    HIGH = 0.85
    LOW  = 0.50

    def __init__(self, db):
        self.queue      = SLAQueue()
        self.reextract  = ReExtractor(db)

    def _enqueue_for_review(self, extraction_id: str, extraction_result: dict,
                             field_name: str, priority: int, sla_minutes: int) -> dict:
        """
        Adapter around SLAQueue.push() — that's the method that actually
        exists on SLAQueue (backend/hitl/sla_queue.py); there is no
        enqueue() method there. push() takes a flat item dict and
        sla_hours (not sla_minutes), so this builds that shape and does
        the unit conversion.
        """
        item = {
            "item_id":       f"{extraction_id}:{field_name}",
            "extraction_id": extraction_id,
            "field_name":    field_name,
            "priority":      priority,
            "result":        extraction_result,
        }
        sla_hours = max(sla_minutes / 60, 1 / 60)  # push() rounds to hours; never pass 0
        self.queue.push(item, sla_hours=sla_hours)
        return item

    def decide(self, extraction_id:str,extraction_result: dict, eval_scores: dict,
               field, doc_hash: str, schema_service) -> dict:
        """
        Takes extraction result + evaluation scores.
        Returns decision + next action.
        """
        score = eval_scores["confidence_score"]
        merged = {**extraction_result, **eval_scores}

        if score >= self.HIGH:
            return {
                "decision":   GateDecision.AUTO_ACCEPT,
                "score":      score,
                "result":     merged,
                "accepted":   True,
            }

        elif score >= self.LOW:
            queue_item = self._enqueue_for_review(
                extraction_id =extraction_id,
                extraction_result = merged,
                field_name        = field.name,
                priority          = self._priority(score),
                sla_minutes       = 60,
            )
            return {
                "decision":   GateDecision.HUMAN_REVIEW,
                "score":      score,
                "queue_item": queue_item,
                "accepted":   False,
            }

        else:
            queue_item = self._enqueue_for_review(
                extraction_id =extraction_id,
                extraction_result=merged,
                field_name=field.name,
                priority=self._priority(score),
                sla_minutes=60,
            )
            retry = self.reextract.retry(
                field          = field,
                doc_hash       = doc_hash,
                schema_service = schema_service,
                attempt        = 1,

            )
            if retry.get("status") == "EXTRACTION_FAILED":
                return {
                    "decision": GateDecision.FAIL,
                    "score": score,
                    "retry": retry,
                    "queue_item": queue_item,
                    "accepted": False,
                }
            return {
                "decision": GateDecision.RE_EXTRACT,
                "score":    score,
                "retry":    retry,
                "queue_item": queue_item,
                "accepted": False,
            }

    def _priority(self, score: float) -> int:
        """Lower confidence = higher urgency = higher priority score."""
        return int((1.0 - score) * 100)