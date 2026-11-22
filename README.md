# Project11 — HITL Extraction Platform

Human-in-the-loop document extraction. Upload a PDF + schema, an AI pipeline extracts fields, low-confidence results go to human reviewers, corrections export as DPO training pairs.

Runs two ways from the same source code — pick one:

| Mode | Guide | Use when |
|---|---|---|
| **Docker** | [DOCKER.md](./DOCKER.md) | You want one command to start everything, no local installs |
| **No Docker** | [SETUP.md](./SETUP.md) | You already have Postgres/Redis/Go/Python installed and want direct process control |

---

## Quick Start — Docker

```bash
cp .env.docker.example .env
# edit .env with your API keys

docker compose up --build
```
→ http://localhost:5173

## Quick Start — No Docker

```bash
cp .env.example .env
# edit .env with your API keys

chmod +x start.sh stop.sh
./start.sh

cd frontend && npm install && npm run dev
```
→ http://localhost:5173

Architecture, HLD/LLD, and workflow traces: **[docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md)**
Python→Go file mapping: **[hitl-go/MIGRATION.md](./hitl-go/MIGRATION.md)**

---

## Project Structure

```
project11/
├── docker-compose.yml          Full stack orchestration (Docker mode)
├── DOCKER.md                   Docker setup guide
├── .env.docker.example         Docker environment config
│
├── start.sh / stop.sh          Process orchestration (no-Docker mode)
├── .env.example                No-Docker environment config
├── SETUP.md                    No-Docker setup guide
│
├── docs/ARCHITECTURE.md        HLD, LLD, workflow diagrams
│
├── hitl-go/                    Go — HTTP server + gateway
│   ├── Dockerfile.server        Builds hitl-server image
│   ├── Dockerfile.gateway       Builds gateway image
│   ├── cmd/server/              API server (port 8000)
│   ├── gateway/                 Auth gateway (port 9000)
│   ├── internal/                auth, db, middleware, queue, backup
│   └── routers/                 8 route handlers
│
├── mailer/                     Go — standalone scheduled email binary
│   └── Dockerfile               Builds mailer image (run on-demand)
│
├── backend/                    Python — Celery workers + ML pipeline
│   ├── Dockerfile               Builds celery-worker image
│   ├── workers/                  celery_app.py, recovery.py
│   ├── services/                  ML pipeline (parser, extractor, evaluator, ...)
│   ├── hitl/                      SLA queue, correction DB, failure tagging
│   ├── monitoring/                drift detection
│   ├── models/db.py               SQLAlchemy models (mirrors Go's db.go)
│   └── utils/                     DB backup, pool management
│
├── frontend/                   React + TypeScript + Vite
│   ├── Dockerfile                Builds frontend dev image
│   └── src/
│       ├── context/AuthContext.tsx
│       ├── lib/api.ts
│       ├── types/index.ts
│       └── pages/
│
├── export/                     DB backups, checkpoints, DPO pairs, mailer config
├── database/                   Local Postgres data dir (if self-managed)
├── uploads/                    Uploaded PDFs + schemas
└── logs/                       Service logs
```

---

## Ports

| Service | Port | Purpose |
|---|---|---|
| Frontend (Vite dev) | 5173 | React UI |
| Go Gateway | 9000 | **Public entry point** — JWT verify, blacklist, proxy |
| Go API Server | 8000 | Internal only — never expose directly |
| Postgres | 5432 | Database |
| Redis | 6379 | Blacklist (DB0) + Celery broker (DB1) |
| Qdrant | 6333 | Vector store |

---

## Why Go + Python

Go owns everything HTTP-facing — fast, small footprint, no ORM overhead. Python owns everything ML — Anthropic/Groq SDKs, Qdrant client, PDF parsing, embeddings. They never call each other directly; they communicate only through Postgres rows and Redis messages (Celery wire protocol), so each can be restarted or scaled independently.

See `docs/ARCHITECTURE.md` for the full request-by-request trace of how a PDF upload becomes a reviewable extraction.
