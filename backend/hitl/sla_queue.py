# backend/hitl/sla_queue.py
"""
SLA-aware review queue backed by Redis sorted set.
Score = SLA deadline unix timestamp → lowest score = most urgent item.

Used by hitl.py routes. Go's hitl.go replaces the HTTP layer but
this class is still used internally by the Python worker pipeline
to push items for human review.
"""
import json
import logging
import os
import time
from datetime import datetime, timedelta

import redis

logger    = logging.getLogger("sla_queue")
REDIS_HOST = os.getenv("REDIS_HOST", "localhost")
REDIS_URL  = os.getenv("REDIS_URL", f"redis://{REDIS_HOST}:6379/0")

QUEUE_KEY       = "hitl:review_queue"
ESCALATION_KEY  = "hitl:escalations"
SLA_HOURS       = int(os.getenv("SLA_HOURS", "24"))


class SLAQueue:
    def __init__(self):
        self._r = redis.from_url(REDIS_URL, decode_responses=True)

    # ── Write ─────────────────────────────────────────────────────────────────

    def push(self, item: dict, sla_hours: int = SLA_HOURS) -> None:
        """
        Push a review item. Score = SLA deadline unix timestamp.
        Lower score = higher priority (expires sooner).
        """
        deadline = time.time() + (sla_hours * 3600)
        item["sla_deadline"] = datetime.utcfromtimestamp(deadline).isoformat()
        item["queued_at"]    = datetime.utcnow().isoformat()
        try:
            self._r.zadd(QUEUE_KEY, {json.dumps(item): deadline})
            logger.info("Queued item_id=%s deadline=%s", item.get("item_id"), item["sla_deadline"])
        except redis.RedisError as e:
            logger.error("Queue push failed: %s", e)

    # ── Read ──────────────────────────────────────────────────────────────────

    def pop_next(self) -> dict | None:
        """
        Atomically pop the highest-priority (lowest score = soonest SLA) item.
        Returns None if queue is empty.
        """
        try:
            results = self._r.zpopmin(QUEUE_KEY, count=1)
            if not results:
                return None
            raw, score = results[0]
            item = json.loads(raw)
            # Flag as escalation if SLA already breached
            if score < time.time():
                self._escalate(item)
            return item
        except redis.RedisError as e:
            logger.error("Queue pop failed: %s", e)
            return None

    def peek(self, limit: int = 50) -> list[dict]:
        """Return up to `limit` items without removing them, ordered by urgency."""
        try:
            results = self._r.zrange(QUEUE_KEY, 0, limit - 1, withscores=True)
            items = []
            now = time.time()
            for raw, score in results:
                item = json.loads(raw)
                item["overdue"] = score < now
                items.append(item)
            return items
        except redis.RedisError as e:
            logger.error("Queue peek failed: %s", e)
            return []

    def depth(self) -> int:
        """Total items in the queue."""
        try:
            return self._r.zcard(QUEUE_KEY)
        except redis.RedisError:
            return 0

    # ── Escalations ───────────────────────────────────────────────────────────

    def _escalate(self, item: dict) -> None:
        """Move an overdue item to the escalation set."""
        try:
            item["escalated_at"] = datetime.utcnow().isoformat()
            self._r.sadd(ESCALATION_KEY, json.dumps(item))
            logger.warning("Escalated overdue item: %s", item.get("item_id"))
        except redis.RedisError as e:
            logger.error("Escalation failed: %s", e)

    def get_escalations(self) -> list[dict]:
        """Return all escalated items."""
        try:
            return [json.loads(raw) for raw in self._r.smembers(ESCALATION_KEY)]
        except redis.RedisError as e:
            logger.error("Get escalations failed: %s", e)
            return []

    def clear_escalations(self) -> dict:
        """Clear all escalation entries (admin action)."""
        try:
            count = self._r.scard(ESCALATION_KEY)
            self._r.delete(ESCALATION_KEY)
            logger.info("Cleared %d escalations", count)
            return {"cleared": True, "count": count}
        except redis.RedisError as e:
            logger.error("Clear escalations failed: %s", e)
            return {"cleared": False, "error": str(e)}

    # ── SLA scan ──────────────────────────────────────────────────────────────

    def scan_overdue(self) -> list[dict]:
        """
        Return all items past their SLA deadline without removing them.
        Called by monitoring/drift_detector.py.
        """
        try:
            now     = time.time()
            results = self._r.zrangebyscore(QUEUE_KEY, "-inf", now, withscores=True)
            return [json.loads(raw) for raw, _ in results]
        except redis.RedisError as e:
            logger.error("Scan overdue failed: %s", e)
            return []
