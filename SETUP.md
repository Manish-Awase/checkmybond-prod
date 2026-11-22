# Setup Guide — No Docker

This project runs as plain local processes. No containers required.

---

## 1. Prerequisites

Install these on your machine before starting:

| Tool | Version | Check |
|---|---|---|
| Go | 1.22+ | `go version` |
| Python | 3.11+ | `python3 --version` |
| Node.js | 18+ | `node --version` |
| Postgres | 14+ | `pg_isready` |
| Redis | 6+ | `redis-cli ping` |
| Qdrant | latest | `curl localhost:6333` |

### macOS (Homebrew)
```bash
brew install go python@3.11 node postgresql@14 redis
brew install qdrant/qdrant/qdrant
brew services start postgresql@14
brew services start redis
```

### Ubuntu/Debian
```bash
sudo apt update
sudo apt install golang-go python3.11 python3-pip nodejs npm postgresql redis-server
sudo service postgresql start
sudo service redis-server start
# Qdrant: download binary from https://github.com/qdrant/qdrant/releases
```

---

## 2. Database Setup

```bash
# Create the database
createdb hitl

# Tables are auto-created on first Celery worker start via
# models/db.py → init_db() → Base.metadata.create_all()
# No manual migration needed for first run.
```

---

## 3. Configure Environment

```bash
cp .env.example .env
# Edit .env — fill in GROQ_API or ANTHROPIC_API_KEY at minimum
```

Load it into your shell before running scripts:
```bash
export $(grep -v '^#' .env | xargs)
```

---

## 4. Install Dependencies

```bash
# Python worker
cd backend
pip install -r requirements.txt --break-system-packages
cd ..

# Go server + gateway
cd hitl-go
go mod tidy
cd ..

# Go mailer
cd mailer
go mod tidy
cd ..

# Frontend
cd frontend
npm install
cd ..
```

---

## 5. Configure Mailer (optional — for scheduled exports)

```bash
# Edit export/email_config.json with real SMTP credentials
nano export/email_config.json
```

---

## 6. Start Everything

```bash
chmod +x start.sh stop.sh
./start.sh
```

This builds and starts, in order:
1. Go mailer binary (`bin/mailer`)
2. Go API server binary (`bin/hitl-server`)
3. Go gateway binary (`bin/hitl-gateway`)
4. Redis (if not already running)
5. Checks Postgres is reachable
6. Celery worker (background, detached)
7. Go API server (background, port 8000)
8. Go gateway (background, port 9000)
9. Registers mailer as a cron job (every 6 hours)

Then start the frontend separately:
```bash
cd frontend
npm run dev
# → http://localhost:5173
```

---

## 7. Verify Everything Is Running

```bash
curl http://localhost:8000/health     # {"status":"ok","service":"hitl-server"}
curl http://localhost:9000/health     # {"status":"ok","service":"gateway"}
redis-cli ping                        # PONG
pg_isready                            # accepting connections
ps aux | grep celery                  # celery worker process visible
```

---

## 8. Stop Everything

```bash
./stop.sh
```

This gracefully stops, in order:
1. Celery worker (warm shutdown — waits for running task, writes checkpoint)
2. Go gateway (SIGTERM, 30s drain)
3. Go API server (SIGTERM, drain, DB backup, connection pool close)
4. Leaves Redis running (used by cron mailer independently)

---

## Common Issues

| Problem | Fix |
|---|---|
| `pg_isready` fails | Postgres not running — `brew services start postgresql@14` or `sudo service postgresql start` |
| `redis-cli ping` fails | Redis not running — `brew services start redis` or `sudo service redis-server start` |
| Go build fails — missing `go.sum` | Run `go mod tidy` inside `hitl-go/` and `mailer/` separately |
| Celery worker crashes on import | Check `requirements.txt` installed with `--break-system-packages` |
| Frontend can't reach API | Confirm gateway is on port 9000 and `vite.config.ts` proxies `/api` there |
| Mailer sends nothing | Edit `export/email_config.json` — placeholder credentials won't work |
