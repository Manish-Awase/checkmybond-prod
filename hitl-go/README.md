# HITL Platform — Go Server

Pure Go replacement for all HTTP-facing Python code.
Python workers (Celery + ML services) remain unchanged.

---

## Structure

```
hitl-go/
├── Makefile
├── go.mod
├── cmd/
│   └── server/
│       └── main.go          # HTTP server entry point — DB init, all routers, graceful shutdown
├── gateway/
│   └── main.go              # API Gateway — JWT verify, Redis blacklist, reverse proxy
├── internal/
│   ├── auth/
│   │   └── auth.go          # bcrypt password hash + JWT create/parse
│   ├── backup/
│   │   └── backup.go        # DB snapshot → export/db_backup_<ts>.json on shutdown
│   ├── db/
│   │   └── db.go            # pgxpool + all SQL queries for every router
│   ├── middleware/
│   │   └── middleware.go    # Identity (gateway headers), AuditLog, RequireRole
│   └── queue/
│       └── celery.go        # Redis enqueue in Celery wire format + blacklist helpers
└── routers/
    ├── helpers.go            # writeJSON, errJSON, decodeJSON
    ├── auth.go               # /api/auth/*
    ├── documents.go          # /api/documents/upload
    ├── extractions.go        # /api/extractions/{id}
    ├── review.go             # /api/review/correct + DPO JSONL export
    ├── queue.go              # /api/queue  /api/queue/all
    ├── monitoring.go         # /api/monitoring/stats
    ├── account.go            # /api/account/* (HTTP Basic owner auth)
    └── hitl.go               # /api/hitl/queue  /queue/next  /escalations/clear
```

---

## Build

```bash
cd hitl-go
go mod tidy
make build
# → bin/hitl-server
# → bin/hitl-gateway
```

---

## Run

```bash
# Terminal 1 — Go HTTP server (port 8000)
make run-server

# Terminal 2 — Go Gateway (port 9000, proxies → 8000)
make run-gateway

# Terminal 3 — Python Celery workers (unchanged)
cd ../backend
celery -A workers.celery_app worker --loglevel=info
```

Frontend connects to **port 9000** (gateway). Everything else is internal.

---

## Environment Variables

### Server
| Variable       | Default                                          | Description              |
|----------------|--------------------------------------------------|--------------------------|
| DATABASE_URL   | postgres://postgres:postgres@localhost:5432/hitl | Postgres DSN             |
| REDIS_HOST     | localhost                                        | Redis hostname           |
| PORT           | 8000                                             | HTTP listen port         |
| EXPORT_DIR     | export                                           | Backup + DPO export dir  |
| UPLOAD_DIR     | uploads                                          | PDF + schema storage     |
| SLA_HOURS      | 24                                               | Hours until SLA deadline |
| OWNER_USERNAME | owner                                            | HTTP Basic owner user    |
| OWNER_PASSWORD | owner@123                                        | HTTP Basic owner pass    |

### Gateway
| Variable            | Default               | Description                        |
|---------------------|-----------------------|------------------------------------|
| BACKEND_URL         | http://localhost:8000 | Go server URL                      |
| JWT_SECRET          | change-me-...         | Must match server JWT_SECRET       |
| REDIS_HOST          | localhost             | Redis for blacklist (DB 0)         |
| GATEWAY_PORT        | 9000                  | Gateway listen port                |
| CORS_ORIGINS        | http://localhost:5173 | Comma-separated allowed origins    |
| EXPOSE_DOCS         | false                 | Set true to expose /docs in dev    |

---

## Language Boundary

```
Go (this repo)                     Python (../backend)
──────────────────────────────     ──────────────────────────────
All HTTP routing                   Celery workers
JWT create + verify                ML extraction pipeline
Postgres reads + writes            Vector store (Qdrant)
Redis blacklist                    LLM calls (Groq/Anthropic)
File upload + save                 PDF parsing (pdfplumber)
DPO JSONL export                   Embeddings (sentence-transformers)
DB backup on shutdown              SLA queue logic
```

Communication: **Go writes `extraction_jobs` rows + pushes to Redis.**
Python reads Redis, runs pipeline, writes `result_json` back to same row.
Go reads `result_json` when frontend polls `/api/extractions/{id}`.
