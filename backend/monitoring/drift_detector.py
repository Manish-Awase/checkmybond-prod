# backend/monitoring/drift_detector.py
"""
Field-level accuracy drift detector.
Compares recent confidence scores (last 7 days) vs baseline (prior 7 days).
Fires alerts when a field's accuracy drops more than DRIFT_THRESHOLD.
"""
import logging
import os
from datetime import datetime, timedelta

from models.db import SessionLocal, Correction

logger          = logging.getLogger("drift_detector")
DRIFT_THRESHOLD = float(os.getenv("DRIFT_THRESHOLD", "0.1"))  # 10% drop triggers alert


class DriftDetector:

    def compute_drift_report(self) -> list[dict]:
        """
        Returns a list of per-field drift entries.
        Each entry has: field_name, recent_avg, baseline_avg, drift, alert.
        """
        db  = SessionLocal()
        now = datetime.utcnow()
        try:
            recent_start   = now - timedelta(days=7)
            baseline_start = now - timedelta(days=14)
            baseline_end   = now - timedelta(days=7)

            # Get all field names that have enough corrections
            fields = (
                db.query(Correction.field_name)
                .filter(Correction.confidence_score.isnot(None))
                .group_by(Correction.field_name)
                .having(db.query(Correction).filter(
                    Correction.field_name == Correction.field_name
                ).count() >= 5)
                .all()
            )

            report = []
            for (field_name,) in fields:
                recent_scores = [
                    c.confidence_score
                    for c in db.query(Correction)
                    .filter(
                        Correction.field_name == field_name,
                        Correction.corrected_at >= recent_start,
                        Correction.confidence_score.isnot(None),
                    ).all()
                ]
                baseline_scores = [
                    c.confidence_score
                    for c in db.query(Correction)
                    .filter(
                        Correction.field_name == field_name,
                        Correction.corrected_at >= baseline_start,
                        Correction.corrected_at < baseline_end,
                        Correction.confidence_score.isnot(None),
                    ).all()
                ]

                if not recent_scores or not baseline_scores:
                    continue

                recent_avg   = sum(recent_scores)   / len(recent_scores)
                baseline_avg = sum(baseline_scores) / len(baseline_scores)
                drift        = baseline_avg - recent_avg   # positive = degraded
                alert        = drift > DRIFT_THRESHOLD

                if alert:
                    logger.warning(
                        "Drift alert | field=%s baseline=%.3f recent=%.3f drift=%.3f",
                        field_name, baseline_avg, recent_avg, drift,
                    )

                report.append({
                    "field_name":   field_name,
                    "recent_avg":   round(recent_avg,   4),
                    "baseline_avg": round(baseline_avg, 4),
                    "drift":        round(drift,        4),
                    "alert":        alert,
                    "recent_n":     len(recent_scores),
                    "baseline_n":   len(baseline_scores),
                })

            return sorted(report, key=lambda x: x["drift"], reverse=True)

        except Exception as e:
            logger.error("Drift computation failed: %s", e)
            return []
        finally:
            db.close()

    def get_alerts(self) -> list[dict]:
        """Return only fields with active drift alerts."""
        return [e for e in self.compute_drift_report() if e["alert"]]
