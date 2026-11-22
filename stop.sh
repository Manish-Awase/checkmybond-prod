#!/usr/bin/env bash
# stop.sh — Gracefully stop all HITL platform services (no Docker).
# Run from the project root: ./stop.sh

set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$ROOT/bin"

echo "=== HITL Platform Shutdown ==="

# ── 1. Stop Celery (warm shutdown — waits for running tasks) ─────────────────
echo "[1/4] Stopping Celery worker (warm shutdown)..."
if [ -f "$ROOT/bin/celery.pid" ]; then
    celery -A workers.celery_app control shutdown 2>/dev/null || true
    sleep 3
fi
pkill -f "celery worker" 2>/dev/null || true
rm -f "$ROOT/bin/celery.pid"
echo "      Celery stopped (checkpoint written to export/checkpoint.json if jobs were running)"

# ── 2. Stop Go gateway (SIGTERM → 30s drain) ──────────────────────────────────
echo "[2/4] Stopping Go gateway..."
if [ -f "$BIN_DIR/gateway.pid" ]; then
    kill -SIGTERM "$(cat "$BIN_DIR/gateway.pid")" 2>/dev/null || true
    rm -f "$BIN_DIR/gateway.pid"
fi
sleep 1
echo "      Gateway stopped"

# ── 3. Stop Go API server (SIGTERM → drain → backup → close) ─────────────────
echo "[3/4] Stopping Go API server..."
if [ -f "$BIN_DIR/server.pid" ]; then
    kill -SIGTERM "$(cat "$BIN_DIR/server.pid")" 2>/dev/null || true
    rm -f "$BIN_DIR/server.pid"
fi
sleep 2   # allow graceful shutdown: drain, backup, pool close
echo "      Server stopped (DB backup written to export/db_backup_<timestamp>.json)"

# ── 4. Redis — leave running (other apps / cron mailer may not need it, but
#     Celery broker uses it; only stop if you are sure nothing else needs it) ─
echo "[4/4] Redis left running (stop manually with: redis-cli shutdown nosave)"

echo ""
echo "=== All services stopped cleanly ==="
echo "    DB backup   → export/db_backup_<timestamp>.json"
echo "    Checkpoint  → export/checkpoint.json (if jobs were interrupted)"
echo "    Resume with: ./start.sh"
