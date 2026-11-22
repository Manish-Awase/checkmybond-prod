# Project11 — Updated File Structure

## Legend
```
✅ created / fixed      ⚠️ missing (needs to be created)
🔄 modified             ❌ deleted / replaced by Go
🆕 new file             🐍 Python only (stays Python)
🐹 Go replacement
```

---

## Full Structure

```
project11/
│
├── docker-compose.yml                        ⚠️ MISSING — no container orchestration yet
│
│
├── gateway/                                  🐹 REPLACED by Go
│   ├── main.py                               ❌ replaced by hitl-go/gateway/main.go
│   └── Dockerfile                            ⚠️ MISSING
│
│
├── hitl-go/                                  🆕 NEW — entire Go server
│   ├── Makefile                              🆕
│   ├── go.mod                                🆕
│   ├── go.sum                                ⚠️ MISSING — run: go mod tidy
│   ├── README.md                             🆕
│   ├── MIGRATION.md                          🆕 Python→Go map
│   │
│   ├── gateway/
│   │   └── main.go                           🆕 replaces gateway/main.py
│   │                                             JWT verify · Redis blacklist · reverse proxy
│   │
│   ├── cmd/
│   │   └── server/
│   │       └── main.go                       🆕 replaces backend/main.py
│   │                                             DB init · router mounts · graceful shutdown
│   │
│   ├── routers/
│   │   ├── helpers.go                        🆕 writeJSON, errJSON, decodeJSON
│   │   ├── auth.go                           🆕 replaces auth/routes_auth.py + routers/auth.py
│   │   ├── documents.go                      🆕 replaces routers/documents.py
│   │   ├── extractions.go                    🆕 replaces routers/extractions.py
│   │   ├── review.go                         🆕 replaces routers/review.py + DPO JSONL export
│   │   ├── queue.go                          🆕 replaces api/route/queue.py
│   │   ├── monitoring.go                     🆕 replaces routers/monitoring.py
│   │   ├── account.go                        🆕 replaces api/route/routes_account.py
│   │   └── hitl.go                           🆕 replaces api/route/hitl.py
│   │                                             (SLA queue via SQL — no SLAQueue class)
│   │
│   └── internal/
│       ├── auth/
│       │   └── auth.go                       🆕 replaces auth/jwt_handler.py
│       │                                         bcrypt · JWT create/parse
│       ├── db/
│       │   └── db.go                         🆕 replaces models/db.py
│       │                                         pgxpool · all SQL queries · no ORM
│       ├── middleware/
│       │   └── middleware.go                 🆕 replaces api/middleware.py + auth/rbac.py
│       │                                         Identity · RequireRole · AuditLog
│       ├── queue/
│       │   └── celery.go                     🆕 replaces auth/redis_blacklist.py
│       │                                         + NEW: Celery wire-format enqueue
│       └── backup/
│           └── backup.go                     🆕 replaces utils/db_backup.py + utils/db_pool.py
│
│
├── mailer/                                   🆕 NEW — Go standalone binary
│   ├── main.go                               🆕 reads export/*.md + *.log → sends email
│   └── go.mod                                🆕 separate module (hitl-mailer)
│
│
├── backend/
│   ├── main.py                               🔄 MODIFIED — lifespan shutdown · middleware
│   │                                             registered · all routers mounted
│   ├── requirements.txt                      🔄 MODIFIED — added sqlalchemy, celery,
│   │                                             redis ⚠️ still missing: passlib,
│   │                                             PyJWT, pydantic[email]
│   │
│   ├── auth/
│   │   ├── __init__.py                       ⚠️ MISSING — Python package marker
│   │   ├── jwt_handler.py                    🔄 MODIFIED — now returns X-Token-Exp
│   │   │                                         X-Token-Hash from gateway headers
│   │   ├── rbac.py                           ✅ analysed — no changes needed
│   │   │                                         ⚠️ NOT in output folder yet
│   │   └── redis_blacklist.py                🔄 MODIFIED — needs is_user_flagged()
│   │                                             ⚠️ NOT in output folder yet
│   │
│   ├── api/
│   │   ├── __init__.py                       ✅ created
│   │   ├── middleware.py                     🔄 MODIFIED — fixed body_hash field name
│   │   │                                         session leak · query_params added
│   │   └── route/
│   │       ├── __init__.py                   ✅ created
│   │       ├── hitl.py                       🔄 MODIFIED — auth added to /escalations/clear
│   │       ├── queue.py                      🔄 MODIFIED — doc_hash nullable fix
│   │       ├── routes_account.py             🔄 MODIFIED — credentials → env vars
│   │       └── routes_auth.py                🔄 MODIFIED — fixed broken import · logout
│   │                                             blacklists token · deactivate blacklists
│   │                                             · require_role() used consistently
│   │
│   ├── services/                             🐍 ALL UNCHANGED — Python only
│   │   ├── parser_service.py                 🐍
│   │   ├── schema_service.py                 🐍
│   │   ├── table_tagger.py                   🐍
│   │   ├── normalizer.py                     🐍
│   │   ├── vector_store.py                   🐍
│   │   ├── extractor.py                      🐍
│   │   ├── evaluator.py                      🐍
│   │   ├── confidence_gate.py                🐍
│   │   ├── reextractor.py                    🐍
│   │   └── prompt_registry.py               🐍
│   │
│   ├── hitl/
│   │   ├── sla_queue.py                      🐍 ⚠️ NOT in output folder
│   │   ├── correction_db.py                  🐍 ⚠️ NOT in output folder
│   │   └── failure_tagger.py                🐍 ⚠️ NOT in output folder
│   │
│   ├── monitoring/
│   │   ├── drift_detector.py                 🐍 ⚠️ NOT in output folder
│   │   └── regression_runner.py              🐍 ⚠️ NOT in output folder
│   │
│   ├── workers/
│   │   ├── __init__.py                       ✅ created
│   │   ├── celery_app.py                     🔄 MODIFIED — acks_late · started_at ·
│   │   │                                         error column · checkpoint on shutdown
│   │   └── recovery.py                       🆕 NEW — re-queues interrupted jobs on startup
│   │
│   ├── models/
│   │   ├── __init__.py                       ✅ created
│   │   └── db.py                             🔄 MODIFIED — datetime.now() → utcnow ·
│   │                                             AuditLog nullable entry_hash ·
│   │                                             body_hash field · sla_deadline ·
│   │                                             started_at · error columns added
│   │                                             CorrectionRecord → Correction
│   │
│   └── utils/                                🆕 NEW directory
│       ├── __init__.py                       🆕
│       ├── db_backup.py                      🆕 all 9 tables → export/db_backup_<ts>.json
│       └── db_pool.py                        🆕 engine.dispose() on shutdown
│
│
├── frontend/                                 ✅ UNCHANGED
│   └── src/
│       ├── App.tsx                           ✅ auth context · router · nav bar
│       ├── main.tsx                          ✅
│       └── pages/
│           ├── LoginPage.tsx                 ✅
│           ├── UploadPage.tsx                ✅
│           ├── ReviewPage.tsx                ✅
│           ├── review.types.ts               🆕 FailureType · helpers extracted
│           └── DashboardPage.tsx             ✅
│
│
├── export/                                   🆕 NEW directory
│   ├── .gitkeep                              ⚠️ MISSING
│   ├── email_config.json                     🆕 SMTP + recipient config
│   ├── db_backup_<timestamp>.json            🆕 written on every graceful shutdown
│   ├── checkpoint.json                       🆕 written if jobs interrupted on shutdown
│   └── dpo_pairs.jsonl                       🆕 written by review.go on each correction
│
├── database/                                 ⚠️ MISSING directory (SQLite default path)
│
├── uploads/                                  ⚠️ MISSING directory (PDF + schema storage)
│
├── logs/                                     ⚠️ MISSING directory
│   ├── celery.log
│   ├── mailer.log
│   └── redis.log
│
├── start.sh                                  🔄 NEEDS UPDATE — still references Python
│                                                 uvicorn only, not Go binaries
└── stop.sh                                   🔄 NEEDS UPDATE — same issue
```

---

## Summary Counts

| Status | Count |
|---|---|
| ✅ Created / fixed | 18 |
| 🆕 New files | 22 |
| 🔄 Modified | 12 |
| ⚠️ Missing (still needed) | 14 |
| ❌ Deleted / replaced | 2 |
| 🐍 Python-only (unchanged) | 13 |

---

## Missing Files Priority

| File | Impact if absent |
|---|---|
| `auth/__init__.py` | Python import crash |
| `auth/rbac.py` (in output) | Python import crash |
| `auth/redis_blacklist.py` (in output) | Python import crash |
| `hitl/sla_queue.py` (in output) | Python import crash |
| `go.sum` | Go build fails |
| `docker-compose.yml` | Cannot deploy — Redis/Postgres hostnames won't resolve |
| `gateway/Dockerfile` | Cannot containerise gateway |
| `database/` dir | SQLite path error on first run |
| `uploads/` dir | File save error on first upload |
| `logs/` dir | Celery log error on start |
| `export/.gitkeep` | Directory not tracked by git |
| `requirements.txt` passlib/PyJWT/pydantic[email] | Auth routes crash |
| `start.sh` Go binary support | Go server never starts |
| `stop.sh` Go binary support | Go server never stops cleanly |
