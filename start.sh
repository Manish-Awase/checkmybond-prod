#!/usr/bin/env bash
# start.sh — Start the full HITL platform (no Docker — plain local processes)
# Run from the project root: ./start.sh

set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
EXPORT_DIR="$ROOT/export"
LOG_DIR="$ROOT/logs"
BIN_DIR="$ROOT/bin"
DB_DIR="$ROOT/database"
UPLOAD_DIR="$ROOT/uploads"

mkdir -p "$EXPORT_DIR" "$LOG_DIR" "$BIN_DIR" "$DB_DIR" "$UPLOAD_DIR"

echo "=== HITL Platform Startup (no Docker) ==="

# ── 1. Build Go mailer binary ─────────────────────────────────────────────────
echo "[1/6] Building Go mailer..."
cd "$ROOT/mailer"
go build -o "$BIN_DIR/mailer" .
echo "      → bin/mailer"

# ── 2. Build Go API server ────────────────────────────────────────────────────
echo "[2/6] Building Go API server..."
cd "$ROOT/hitl-go"
go build -o "$BIN_DIR/hitl-server" ./cmd/server
echo "      → bin/hitl-server"

# ── 3. Build Go gateway ────────────────────────────────────────────────────────
echo "[3/6] Building Go gateway..."
cd "$ROOT/hitl-go"
go build -o "$BIN_DIR/hitl-gateway" ./gateway
echo "      → bin/hitl-gateway"

# ── 4. Check Redis ────────────────────────────────────────────────────────────
echo "[4/6] Checking Redis..."
if ! redis-cli ping > /dev/null 2>&1; then
    echo "      Starting Redis..."
    redis-server --daemonize yes --logfile "$LOG_DIR/redis.log"
    sleep 1
else
    echo "      Redis already running"
fi

# ── 5. Check Postgres ─────────────────────────────────────────────────────────
echo "[5/6] Checking Postgres..."
if ! pg_isready -h localhost -p 5432 > /dev/null 2>&1; then
    echo "      WARNING: Postgres not reachable on localhost:5432"
    echo "      Start it manually: pg_ctl -D /usr/local/var/postgres start"
    echo "      Or install: sudo apt install postgresql && sudo service postgresql start"
else
    echo "      Postgres already running"
fi

# ── 6. Start Celery worker ────────────────────────────────────────────────────
echo "[6/6] Starting Celery worker..."
cd "$ROOT/backend"
celery -A workers.celery_app worker \
    --loglevel=info \
    --concurrency=2 \
    --logfile="$LOG_DIR/celery.log" \
    --pidfile="$ROOT/bin/celery.pid" \
    --detach
echo "      Celery worker started (log → logs/celery.log)"

# ── Start Go API server ────────────────────────────────────────────────────────
echo "Starting Go API server..."
EXPORT_DIR="$EXPORT_DIR" \
DATABASE_URL="${DATABASE_URL:-postgres://postgres:postgres@localhost:5432/hitl}" \
REDIS_HOST="${REDIS_HOST:-localhost}" \
UPLOAD_DIR="$UPLOAD_DIR" \
PORT=8000 \
"$BIN_DIR/hitl-server" >> "$LOG_DIR/hitl-server.log" 2>&1 &
SERVER_PID=$!
echo "      hitl-server started (PID $SERVER_PID)"

sleep 1

# ── Start Go gateway ───────────────────────────────────────────────────────────
echo "Starting Go gateway..."
BACKEND_URL="http://localhost:8000" \
JWT_SECRET="${JWT_SECRET:-change-me-in-production-256bit}" \
REDIS_HOST="${REDIS_HOST:-localhost}" \
GATEWAY_PORT=9000 \
CORS_ORIGINS="${CORS_ORIGINS:-http://localhost:5173}" \
"$BIN_DIR/hitl-gateway" >> "$LOG_DIR/hitl-gateway.log" 2>&1 &
GATEWAY_PID=$!
echo "      hitl-gateway started (PID $GATEWAY_PID)"

echo "$SERVER_PID"  > "$BIN_DIR/server.pid"
echo "$GATEWAY_PID" > "$BIN_DIR/gateway.pid"

# ── Register mailer cron job ──────────────────────────────────────────────────
CRON_LINE="0 */6 * * * EXPORT_DIR=$EXPORT_DIR $BIN_DIR/mailer >> $LOG_DIR/mailer.log 2>&1"
( crontab -l 2>/dev/null | grep -qF "$BIN_DIR/mailer" ) || (
    ( crontab -l 2>/dev/null; echo "$CRON_LINE" ) | crontab -
    echo "Cron job registered: mailer every 6 hours"
)

echo ""
echo "=== All services started ==="
echo "    Go API Server  → http://localhost:8000  (internal — not for direct use)"
echo "    Go Gateway     → http://localhost:9000  (use this from frontend)"
echo "    Frontend       → http://localhost:5173  (run: cd frontend && npm run dev)"
echo "    Mailer         → cron every 6 hours"
echo "    Logs           → $LOG_DIR/"
echo "    PIDs           → $BIN_DIR/*.pid"
echo ""
echo "To stop all: ./stop.sh"
