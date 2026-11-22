# Python → Go Migration Map

## File Replacement Table

| Python File | Go Replacement | Notes |
|---|---|---|
| `gateway/main.py` | `gateway/main.go` | JWT verify, Redis blacklist, reverse proxy |
| `backend/main.py` | `cmd/server/main.go` | Server entry, DB init, router mounts, graceful shutdown |
| `routers/auth.py` | `routers/auth.go` | Stub replaced by full implementation |
| `routers/documents.py` | `routers/documents.go` | Stub replaced by full implementation |
| `routers/extractions.py` | `routers/extractions.go` | Stub replaced by full implementation |
| `routers/review.py` | `routers/review.go` | Stub replaced, adds DPO JSONL export |
| `routers/monitoring.py` | `routers/monitoring.go` | Stub replaced by real SQL aggregation |
| `api/routes/queue.py` | `routers/queue.go` | Full replacement |
| `api/route/routes_account.py` | `routers/account.go` | Full replacement |
| `api/route/hitl.py` | `routers/hitl.go` | SLA queue via SQL — no SLAQueue class needed |
| `auth/routes_auth.py` | `routers/auth.go` | Merged into single auth router |
| `auth/jwt_handler.py` | `internal/auth/auth.go` | bcrypt + JWT create/parse |
| `auth/rbac.py` | `internal/middleware/middleware.go` | `RequireRole()` middleware |
| `auth/redis_blacklist.py` | `internal/queue/celery.go` | Blacklist helpers in same file as queue |
| `api/middleware.py` | `internal/middleware/middleware.go` | `AuditLog` middleware |
| `models/db.py` | `internal/db/db.go` | pgxpool + all SQL queries (no ORM) |
| `utils/db_backup.py` | `internal/backup/backup.go` | All 8 tables snapshot on shutdown |
| `utils/db_pool.py` | `cmd/server/main.go` | `idb.Close()` called in shutdown block |

---

## Files With No Go Equivalent (Python Only)

| Python File | Why It Stays Python |
|---|---|
| `workers/celery_app.py` | Celery is Python-only |
| `workers/recovery.py` | Imports Celery task directly |
| `services/extractor.py` | Anthropic/Groq SDK, regex parsing |
| `services/parser_service.py` | PDF parsing (pdfplumber, pytesseract) |
| `services/vector_store.py` | Qdrant client, sentence-transformers |
| `services/evaluator.py` | ML scoring logic |
| `services/confidence_gate.py` | Business logic on ML scores |
| `hitl/sla_queue.py` | Internal queue — no HTTP |

---

## New Go Files With No Python Equivalent

| Go File | What It Does |
|---|---|
| `internal/queue/celery.go` | Enqueues tasks to Redis in Celery wire format |
| `mailer/main.go` | Scheduled email sender (cron every 6h) |
| `Makefile` | Build automation |

---

## Architecture Diagram

```
Internet
   │
   ▼
┌─────────────────────────────────┐
│  gateway/main.go                │  ← replaces gateway/main.py
│  JWT verify · blacklist · proxy │
└────────────────┬────────────────┘
                 │ HTTP  (injects X-User-Id, X-User-Role,
                 │        X-Token-Exp, X-Token-Hash)
┌────────────────▼────────────────┐
│  cmd/server/main.go             │  ← replaces backend/main.py
│                                 │
│  routers/                       │  ← replaces backend/routers/ +
│    auth.go                      │    backend/api/route/ +
│    documents.go                 │    backend/auth/routes_auth.py
│    extractions.go               │
│    review.go                    │
│    queue.go                     │
│    monitoring.go                │
│    account.go                   │
│    hitl.go                      │
│                                 │
│  internal/                      │
│    auth/auth.go                 │  ← replaces auth/jwt_handler.py
│    db/db.go                     │  ← replaces models/db.py (pgx, no ORM)
│    middleware/middleware.go      │  ← replaces api/middleware.py + auth/rbac.py
│    queue/celery.go              │  ← replaces auth/redis_blacklist.py + NEW
│    backup/backup.go             │  ← replaces utils/db_backup.py
└──────┬──────────────────────────┘
       │ SQL (pgx)          │ Redis LPUSH
       ▼                    ▼
  ┌─────────┐         ┌─────────┐
  │Postgres │         │  Redis  │
  └─────────┘         └─────────┘
       │                    │ Redis BRPOP
       │ SQL           ┌────▼────────────────────────┐
       │               │  Python Workers (UNCHANGED)  │
       └──────────────►│  workers/celery_app.py       │
                       │  services/extractor.py       │
                       │  services/parser_service.py  │
                       │  services/vector_store.py    │
                       │  services/evaluator.py       │
                       └─────────────────────────────┘
```

---

## Internal Package Map

```
internal/
├── auth/auth.go          bcrypt hash/verify · JWT create/parse
│                         replaces: auth/jwt_handler.py (passlib + PyJWT)
│
├── db/db.go              pgxpool · all SQL queries · model structs
│                         replaces: models/db.py (SQLAlchemy ORM)
│
├── middleware/            
│   middleware.go         Identity · RequireRole · AuditLog
│                         replaces: api/middleware.py + auth/rbac.py
│
├── queue/celery.go       Celery wire-format enqueue · blacklist helpers
│                         replaces: auth/redis_blacklist.py
│                         new: Celery message builder (no Python equivalent)
│
└── backup/backup.go      All-table DB snapshot on shutdown
                          replaces: utils/db_backup.py + utils/db_pool.py
```

---

## Key Behavioural Differences

| Behaviour | Python | Go |
|---|---|---|
| ORM | SQLAlchemy declarative models | Raw SQL via pgx — no ORM |
| Password hashing | `passlib[bcrypt]` | `golang.org/x/crypto/bcrypt` |
| JWT | `python-jose` / `PyJWT` | `golang-jwt/jwt` |
| RBAC | `require_role()` FastAPI Depends | `RequireRole()` chi middleware |
| Audit log session | `next(get_db())` generator | `SessionLocal()` with explicit close |
| SLA queue | Python `SLAQueue` class | Direct SQL `ORDER BY sla_deadline ASC` |
| DPO export | Implicit (stub) | Explicit JSONL append in `review.go` |
| DB backup | 9-table Python loop | 8-table Go loop via `BackupTable()` |
| Shutdown | FastAPI `lifespan` + `asyncio.sleep` | `signal.Notify` + `srv.Shutdown(ctx)` |
