# Project11 — HITL Extraction Platform
## High-Level Design · Low-Level Design · Workflow

---

# 1. High-Level Design (HLD)

## 1.1 System Overview

Project11 is a Human-In-The-Loop document extraction platform. Users upload a PDF and a field schema; an AI pipeline extracts structured fields; low-confidence extractions are routed to human reviewers; corrections are exported as DPO training pairs to improve the model over time.

## 1.2 Architecture Diagram

```
                              ┌─────────────────────┐
                              │   React Frontend     │
                              │   (Vite, port 5173)  │
                              └──────────┬───────────┘
                                         │ HTTPS
                              ┌──────────▼───────────┐
                              │   Go Gateway          │
                              │   (port 9000)         │
                              │   JWT verify          │
                              │   Redis blacklist     │
                              │   Reverse proxy       │
                              └──────────┬───────────┘
                                         │ HTTP + trusted headers
                              ┌──────────▼───────────┐
                              │   Go API Server       │
                              │   (port 8000)         │
                              │   8 routers           │
                              └─────┬────────────┬────┘
                                   │ SQL          │ Redis LPUSH
                        ┌──────────▼───┐    ┌─────▼──────┐
                        │  Postgres    │    │   Redis     │
                        │  (9 tables)  │    │  DB0:blklst │
                        └──────────▲───┘    │  DB1:broker │
                                   │ SQL     └─────┬──────┘
                                   │               │ BRPOP
                        ┌──────────┴───────────────▼────┐
                        │   Python Celery Workers         │
                        │   parser → normalizer →         │
                        │   vector_store → extractor →    │
                        │   evaluator → confidence_gate   │
                        └──────────────┬──────────────────┘
                                      │
                        ┌─────────────▼─────────────┐
                        │  Qdrant (vector store)      │
                        │  Anthropic/Groq (LLM)        │
                        └────────────────────────────┘

        ┌─────────────────────────────────────────────┐
        │  Go Mailer (standalone, cron every 6h)        │
        │  reads export/*.md *.log → emails team        │
        └─────────────────────────────────────────────┘
```

## 1.3 Component Responsibilities

| Component | Language | Responsibility |
|---|---|---|
| Frontend | React/TS | Upload UI, review UI, dashboard |
| Gateway | Go | Auth boundary — the only internet-facing service |
| API Server | Go | All CRUD, business rules, job orchestration |
| Celery Workers | Python | PDF parsing, embeddings, LLM extraction, scoring |
| Postgres | — | System of record — users, jobs, corrections, audit |
| Redis | — | Token blacklist (DB0) + Celery broker/backend (DB1/DB2) |
| Qdrant | — | Vector similarity search for chunk retrieval |
| Mailer | Go | Scheduled export delivery via cron |

## 1.4 Why This Split

Go owns everything synchronous and HTTP-facing — it is fast, has a small footprint, and needs no ORM. Python owns everything ML — it has the SDKs (Anthropic, Groq, Qdrant, sentence-transformers) Go does not. The two never call each other directly; they communicate only through Postgres rows and Redis messages, so each side can be deployed, scaled, and restarted independently.

---

# 2. Low-Level Design (LLD)

## 2.1 Database Schema (Postgres — `internal/db/db.go` / `models/db.py`)

| Table | Key Columns | Written By | Read By |
|---|---|---|---|
| `users` | id, username, email, password_hash, role, is_active | `routers/auth.go` | `routers/auth.go`, gateway |
| `documents` | doc_id, doc_hash, pdf_path, schema_path, user_id | `routers/documents.go` | — |
| `schema_registry` | schema_id, definition_json | `routers/documents.go` | `services/schema_service.py` |
| `extraction_jobs` | extraction_id, status, result_json, sla_deadline | Go (create) + Python (update) | `routers/extractions.go`, `routers/queue.go`, `routers/hitl.go` |
| `extractions` | extraction_id, fields_json, overall_confidence | Python workers | — |
| `corrections` | id, field_name, corrected_value, failure_type | `routers/review.go` | `routers/monitoring.go` |
| `audit_log` | path, method, user_id, status_code, body_hash | `internal/middleware/middleware.go` | — |
| `chunks` | point_id, doc_hash, text | `services/vector_store.py` | `services/extractor.py` |
| `prompt_versions` | prompt_id, body, is_active | `services/prompt_registry.py` | `services/extractor.py` |

## 2.2 Redis Key Design

| Key Pattern | DB | Set By | Checked By |
|---|---|---|---|
| `blacklist:token:<sha256>` | 0 | `BlacklistToken()` (Go), `blacklist_user_tokens()` (Python) | `isBlacklisted()` in gateway |
| `blacklist:user_flag:<id>` | 0 | `BlacklistUserFlag()` (Go) | `isBlacklisted()` in gateway |
| `celery` (list) | 1 | `EnqueueExtractionJob()` (Go) | Celery worker `BRPOP` |
| `hitl:review_queue` (zset) | 0 | `SLAQueue.push()` (Python) | `SLAQueue.pop_next()` |
| `hitl:escalations` (set) | 0 | `SLAQueue._escalate()` | `SLAQueue.get_escalations()` |

## 2.3 Go Server Module Map

```
hitl-go/
├── gateway/main.go          gatewayHandler() → verifyJWT() → isBlacklisted() → proxy()
├── cmd/server/main.go       main() → idb.Init() → iqueue.Init() → mounts 8 routers
├── routers/*.go             one file per resource, each exports a XxxRouter() http.Handler
└── internal/
    ├── auth/auth.go         HashPassword · VerifyPassword · CreateAccessToken · ParseToken
    ├── db/db.go              all SQL queries, returns typed structs
    ├── middleware/           Identity · RequireRole · AuditLog
    ├── queue/celery.go       EnqueueExtractionJob · Blacklist* functions
    └── backup/backup.go      Run() — snapshot all tables on shutdown
```

## 2.4 Python Worker Module Map

```
backend/
├── workers/
│   ├── celery_app.py        process_document() — the only Celery task
│   └── recovery.py          recover_incomplete_jobs() — startup re-queue
├── services/                 (excluded from doc detail — ML internals)
├── hitl/
│   ├── sla_queue.py          SLAQueue class — push/pop_next/peek/escalate
│   ├── correction_db.py      save_correction()
│   └── failure_tagger.py    infer_failure_type()
├── monitoring/
│   └── drift_detector.py     DriftDetector.compute_drift_report()
└── models/db.py               SQLAlchemy models — same tables as Go's db.go
```

---

# 3. Workflow — Function-Level Trace

## 3.1 Workflow A — User Registration & Login

```
Frontend                Gateway                  Go Server                 Postgres
────────                ───────                  ──────────                ────────
LoginPage.tsx
  handleLogin()
    │
    ▼
AuthContext.tsx
  login(loginVal, password)
    │ POST /api/auth/login
    ▼
                         gatewayHandler()
                           path is public →
                           skip verifyJWT()
                           proxy(w, r, nil)
                                                   │
                                                   ▼
                                                  AuthRouter()
                                                    handleLogin()
                                                      idb.GetUserByUsername()
                                                      idb.GetUserByEmail()  ──────► SELECT users
                                                      iauth.VerifyPassword()
                                                      idb.UpdateLastLogin() ──────► UPDATE users
                                                      iauth.CreateAccessToken()
                                                   │
                         ◄─────────────────────────┘ {access_token, role, ...}
AuthContext.tsx
  _persist(authUser)
  → localStorage["hitl_auth"]
    │
    ▼
LoginPage.tsx
  navigate("/upload" | "/dashboard")
```

**Connecting functions:** `LoginPage.handleLogin()` → `AuthContext.login()` → `gatewayHandler()` → `verifyJWT()` *(skipped, public path)* → `routers.AuthRouter().handleLogin()` → `idb.GetUserByUsername()` → `iauth.VerifyPassword()` → `iauth.CreateAccessToken()`.

---

## 3.2 Workflow B — Document Upload → Extraction Pipeline

This is the core workflow connecting Go and Python through Postgres and Redis.

```
Frontend              Gateway          Go Server                  Postgres    Redis        Celery Worker
────────              ───────          ──────────                 ────────    ─────        ─────────────
UploadPage.tsx
  uploadMutation
    │ api.ts: uploadDocument(formData)
    │ POST /api/documents/upload
    │ Authorization: Bearer <token>
    ▼
                       gatewayHandler()
                         verifyJWT(authHeader)
                           → claims{sub, role, exp}
                         isBlacklisted(token, userID)
                           → rdb.Exists() on DB0
                         proxy() injects:
                           X-User-Id, X-User-Role,
                           X-Token-Exp, X-Token-Hash
                                                   │
                                                   ▼
                                                  DocumentsRouter()
                                                    imw.Identity()
                                                      reads X-User-Id etc
                                                      → context
                                                    handleUpload()
                                                      r.ParseMultipartForm()
                                                      sha256(pdfBytes) → docHash
                                                      os.WriteFile(pdfPath)
                                                      sha256(schemaBytes) → schemaID
                                                      idb.Pool().Exec()
                                                        INSERT schema_registry ───► INSERT
                                                      idb.Pool().Exec()
                                                        INSERT documents      ───► INSERT
                                                      idb.CreateExtractionJob()
                                                        INSERT extraction_jobs ──► INSERT
                                                          (status='pending',
                                                           sla_deadline=+24h)
                                                      iqueue.EnqueueExtractionJob()
                                                        builds Celery message
                                                        (base64 JSON body,
                                                         task=workers.celery_app
                                                              .process_document)
                                                                              ────► LPUSH "celery"
                       ◄─────────────────────────────────────────────────────┘
                       {extraction_id, status:"processing"}
UploadPage.tsx
  navigate(`/review/${extraction_id}`)


                                                                               BRPOP "celery" ◄────┘
                                                                                                    │
                                                                                                    ▼
                                                                                          worker_init signal
                                                                                            on_worker_start()
                                                                                              init_db()
                                                                                                    │
                                                                                                    ▼
                                                                                          task_prerun signal
                                                                                            on_task_start()
                                                                                                    │
                                                                                                    ▼
                                                                                          process_document(
                                                                                            extraction_id,
                                                                                            pdf_path,
                                                                                            schema_id,
                                                                                            doc_hash
                                                                                          )
                                                                                            job.status="processing"
                                                                                            job.started_at=utcnow()
                                                                            UPDATE ◄───────────  db.commit()
                                                                                                    │
                                                                                                    ▼
                                                                              parser.parse()
                                                                              normalizer.chunk_document()
                                                                              vs.index_chunks()
                                                                              for field in schema.fields:
                                                                                extractor.extract_field()
                                                                                evaluator.evaluate()
                                                                                gate.decide()
                                                                              job.result_json = json.dumps(...)
                                                                              job.status = "complete" |
                                                                                           "pending" (needs review)
                                                                            UPDATE ◄───────────  db.commit()
                                                                                                    │
                                                                                                    ▼
                                                                                          task_postrun signal
                                                                                            on_task_end()
```

**Connecting functions, in call order:**

`UploadPage.uploadMutation` → `api.uploadDocument()` → `gatewayHandler()` → `verifyJWT()` → `isBlacklisted()` → `proxy()` → `imw.Identity()` → `routers.handleUpload()` → `idb.CreateExtractionJob()` → `iqueue.EnqueueExtractionJob()` → *(Redis LPUSH)* → *(Celery BRPOP)* → `on_worker_start()` → `on_task_start()` → `process_document()` → `ParserService.parse()` → `Normalizer.chunk_document()` → `VectorStoreService.index_chunks()` → `FieldExtractor.extract_field()` → `RAGEvaluator.evaluate()` → `ConfidenceGate.decide()` → `on_task_end()`.

---

## 3.3 Workflow C — Polling for Extraction Results

```
Frontend                              Go Server                    Postgres
────────                              ──────────                   ────────
ReviewPage.tsx
  useQuery(refetchInterval: 2000)
    │ api.ts: getExtraction(id)
    │ GET /api/extractions/{id}
    ▼
                                       ExtractionsRouter()
                                         imw.Identity()
                                         handleGetExtraction()
                                           idb.GetExtractionJob(id) ────► SELECT extraction_jobs
                                           idb.ParseFieldResults(
                                             job.ResultJSON)
                  ◄────────────────────────┘ {status, fields[], sla_deadline}

  if status === "complete" || "failed":
    refetchInterval → false  (stop polling)
  else:
    poll again in 2s
```

**Connecting functions:** `ReviewPage` `useQuery` → `api.getExtraction()` → `routers.handleGetExtraction()` → `idb.GetExtractionJob()` → `idb.ParseFieldResults()`.

---

## 3.4 Workflow D — Human Correction → DPO Export

```
Frontend                        Go Server                       Postgres        File System
────────                        ──────────                      ────────        ───────────
ReviewPage.tsx
  correctMutation.mutate({...})
    │ api.ts: submitCorrection(payload)
    │ POST /api/review/correct
    ▼
                                 ReviewRouter()
                                   imw.Identity()
                                   handleCorrect()
                                     idb.GetExtractionJob()  ──► SELECT (get doc_hash)
                                     idb.CreateCorrection()  ──► INSERT corrections
                                     exportDPOPair()
                                       json.Marshal(pair)
                                       os.OpenFile(append)                  ──► export/dpo_pairs.jsonl
               ◄───────────────────┘ {status:"saved", dpo_pair_exported:true}
ReviewPage.tsx
  setSubmitted(field_name)
  qc.invalidateQueries(["extraction", id])
  → triggers re-fetch via Workflow C
```

**Connecting functions:** `ReviewPage.correctMutation` → `api.submitCorrection()` → `routers.handleCorrect()` → `idb.GetExtractionJob()` → `idb.CreateCorrection()` → `exportDPOPair()`.

---

## 3.5 Workflow E — Graceful Shutdown & Recovery

```
Go Server                              Postgres/Redis              Python Worker
──────────                             ──────────────               ─────────────
SIGTERM received
  main() <-quit channel
    srv.Shutdown(ctx, 30s timeout)
      → drains active HTTP requests
    ibackup.Run(ctx)
      queries 8 tables          ────► SELECT * FROM ...
      writes export/db_backup_<ts>.json
    idb.Close()                 ────► pool closed
    iqueue.Close()              ────► redis closed


                                                                    SIGTERM received
                                                                      Celery warm shutdown
                                                                        waits for running task
                                                                        to finish (acks_late=True)
                                                                      worker_shutdown signal
                                                                        on_worker_shutdown()
                                                                          _write_checkpoint()
                                                                            queries incomplete jobs
                                                  export/checkpoint.json ◄── writes JSON


───────────────────────────  ON RESTART  ───────────────────────────

                                                                    celery worker starts
                                                                      worker_init signal
                                                                        on_worker_start()
                                                                          init_db()

Go server starts
  main() → idb.Init()
                                                                    (separately, Python side)
                                                                    recovery.recover_incomplete_jobs()
                                                  export/checkpoint.json ──► read
                                                                      for each incomplete job:
                                                                        process_document.delay(...)
                                                                              ───────► LPUSH "celery"
                                                                      os.remove(checkpoint.json)
```

**Connecting functions:** `main()` *(Go)* → `srv.Shutdown()` → `ibackup.Run()` → `idb.Close()` / `iqueue.Close()`. Separately: `on_worker_shutdown()` → `_write_checkpoint()`; on restart: `recover_incomplete_jobs()` → `process_document.delay()`.

---

## 3.6 Workflow F — Logout / Token Blacklist

```
Frontend                  Gateway                    Go Server              Redis
────────                  ───────                     ──────────             ─────
AuthContext.logout()
  POST /api/auth/logout
  Authorization: Bearer <token>
    │
    ▼
                           gatewayHandler()
                             verifyJWT() → claims
                             isBlacklisted() → false (not yet)
                             proxy() injects
                               X-Token-Hash, X-Token-Exp
                                                       │
                                                       ▼
                                                      AuthRouter()
                                                        imw.Identity()
                                                          reads token_hash, token_exp
                                                        handleLogout()
                                                          iqueue.BlacklistToken(
                                                            tokenHash, userID, ttl)
                                                                              ──► SETEX
                                                                                  blacklist:token:<hash>
localStorage.removeItem("hitl_auth")


                           ── any subsequent request with same token ──
                           gatewayHandler()
                             verifyJWT() → still valid (not expired)
                             isBlacklisted(token, userID)
                                                                          ──► EXISTS
                                                                              blacklist:token:<hash>
                                                                              → true
                             return 401 "Account has been deactivated"
```

**Connecting functions:** `AuthContext.logout()` → `gatewayHandler()` → `routers.handleLogout()` → `iqueue.BlacklistToken()`. Next request: `gatewayHandler()` → `isBlacklisted()` → reject.

---

# 4. Summary — Critical Connector Functions

| Function | File | Role |
|---|---|---|
| `gatewayHandler()` | `gateway/main.go` | Single entry point — every request passes through this |
| `verifyJWT()` | `gateway/main.go` | Decodes and validates JWT, extracts claims |
| `isBlacklisted()` | `gateway/main.go` | Dual Redis check — blocks deactivated/logged-out users |
| `proxy()` | `gateway/main.go` | Forwards request to Go server with trusted headers |
| `imw.Identity()` | `internal/middleware/middleware.go` | Reads gateway headers into Go request context |
| `idb.CreateExtractionJob()` | `internal/db/db.go` | Writes the row Python will later update |
| `iqueue.EnqueueExtractionJob()` | `internal/queue/celery.go` | The Go→Python handoff — Celery wire format |
| `process_document()` | `workers/celery_app.py` | The Python→Go handoff — writes `result_json` back |
| `idb.GetExtractionJob()` | `internal/db/db.go` | How frontend polling reads Python's output |
| `exportDPOPair()` | `routers/review.go` | Converts a correction into a training artifact |
| `recover_incomplete_jobs()` | `workers/recovery.py` | Closes the loop after an unclean shutdown |
