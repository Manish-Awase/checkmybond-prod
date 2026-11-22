# Docker Setup Guide

This is the containerized version of Project11. For the plain-process (no Docker) setup, see [SETUP.md](./SETUP.md) instead — both exist side by side and use the same source code.

---

## Prerequisites

| Tool | Version |
|---|---|
| Docker | 24+ |
| Docker Compose | v2 (bundled with Docker Desktop) |

That's it — Postgres, Redis, Qdrant, Go, Python, and Node all run inside containers. Nothing else needs to be installed on the host.

---

## 1. Configure Environment

```bash
cp .env.docker.example .env
# Edit .env — set GROQ_API or ANTHROPIC_API_KEY at minimum
```

---

## 2. Configure Mailer (optional)

```bash
nano export/email_config.json
# Fill in real SMTP credentials before running the mailer service
```

---

## 3. Build and Start

```bash
docker compose up --build
```

This starts, in dependency order:

```
postgres ──┐
           ├──► hitl-server ──► gateway ──► frontend
redis ─────┤
           │
qdrant ────┘
           └──► celery-worker
```

Health checks gate startup — `hitl-server` won't start until Postgres and Redis report healthy; `gateway` won't start until `hitl-server` is healthy.

Run in the background instead:
```bash
docker compose up --build -d
```

---

## 4. Verify

```bash
docker compose ps
# All services should show "healthy" or "running"

curl http://localhost:8000/health
curl http://localhost:9000/health
```

Open the app: **http://localhost:5173**

---

## 5. View Logs

```bash
docker compose logs -f                  # all services
docker compose logs -f celery-worker    # just the worker
docker compose logs -f gateway          # just the gateway
```

---

## 6. Mailer — Automatic vs Manual Behavior

**The mailer never runs automatically just because Docker is running.** This is intentional, and the mechanism behind it is worth understanding fully so you don't assume emails are being sent when they aren't.

### How `profiles: ["manual"]` actually works

In `docker-compose.yml`, the `mailer` service is tagged:
```yaml
mailer:
  ...
  profiles: ["manual"]
```

Docker Compose's rule is simple: **services with no `profiles:` key always start with `docker compose up`. Services with a `profiles:` key only start if that profile is explicitly activated.** `"manual"` is not a special keyword recognized by Compose — it is an arbitrary label. Any string works the same way; the name only matters for you to remember what it's for.

This means:

```bash
docker compose up           # mailer does NOT start
docker compose up -d        # mailer does NOT start
docker compose up --build   # mailer does NOT start
```

None of these will ever build, start, send an email, or do anything with the `mailer` service — Compose skips it entirely as if it weren't in the file.

### The only two ways it runs

| Command | What happens |
|---|---|
| `docker compose run --rm mailer` | Runs once, sends if conditions are met, exits, container is removed (`--rm`) |
| `docker compose --profile manual up mailer` | Activates the `manual` profile, starts `mailer` (and would start any other service sharing that profile) |

`docker compose run --rm mailer` works **without** needing `--profile manual` at all — explicitly naming a service on the command line runs it regardless of its profile. This is documented Compose behavior, not a quirk of this project.

### There is no scheduler inside Docker Compose

Compose has no built-in cron, timer, or "every N hours" mechanism. `profiles: ["manual"]` only controls whether a service starts with `up` — it has nothing to do with scheduling. To actually get automatic recurring emails, you must register a **host-level cron job** that calls the command yourself:

```bash
crontab -e
# add:
0 */6 * * * cd /path/to/project11 && docker compose run --rm mailer >> logs/mailer.log 2>&1
```

Without this cron entry, the mailer will never send anything automatically — no matter how long the rest of the stack stays up.

### What happens if you change or remove the profile

| `profiles:` value | Behavior with `docker compose up` |
|---|---|
| `["manual"]` (current) | Never starts automatically — must be run explicitly |
| `[]` or omitted entirely | **Starts immediately on every `docker compose up`**, and since the mailer binary exits after one send, it will show as `Exited (0)` in `docker compose ps` right away — it will NOT loop or retry on a schedule by itself |
| `["dev"]` or any other custom name | Same as `"manual"` — only starts with `docker compose --profile dev up`, or by explicitly targeting it with `run`/`up mailer` |
| Service depended on by an always-on service via `depends_on` | The profile is force-activated for that one startup, even without `--profile` — avoid this for `mailer` unless you want it firing every time another service starts |

**If you remove `profiles: ["manual"]` entirely**, do not expect repeating sends — the container will run once at `docker compose up` time, send (or fail silently per `email_config.json`), exit, and then sit stopped. You would still need host cron (or `restart: unless-stopped` plus your own in-binary sleep loop, which this project's `mailer/main.go` does not implement) to get repeated sends.

### Stop / disable automatic sending

Since automation only ever comes from your own cron entry (never from Compose itself), stopping it is purely a crontab edit:
```bash
crontab -l | grep -v "docker compose run --rm mailer" | crontab -
```

---

## 7. Stop Everything

```bash
docker compose down
```

This stops containers but **keeps data** (Postgres volume, Qdrant volume, `export/`, `uploads/` are all bind-mounted or named volumes).

### Graceful shutdown behavior
- `hitl-server` and `gateway` receive `SIGTERM` from Docker, triggering the same Go graceful shutdown (drain → backup → close) as the non-Docker setup.
- `celery-worker` receives `SIGTERM`, triggers Celery's warm shutdown (waits for running task, writes checkpoint).

To also wipe all data (fresh start):
```bash
docker compose down -v
```

---

## Service Reference

| Service | Container Name | Port | Healthcheck |
|---|---|---|---|
| postgres | hitl-postgres | 5432 | `pg_isready` |
| redis | hitl-redis | 6379 | `redis-cli ping` |
| qdrant | hitl-qdrant | 6333 | — |
| hitl-server | hitl-server | 8000 | `GET /health` |
| gateway | hitl-gateway | 9000 | `GET /health` |
| celery-worker | hitl-celery-worker | — | `celery inspect ping` |
| frontend | hitl-frontend | 5173 | — |
| mailer | hitl-mailer | — (one-shot) | — |

---

## Rebuilding After Code Changes

```bash
# Rebuild one service
docker compose up --build hitl-server

# Rebuild everything
docker compose build --no-cache
docker compose up
```

---

## Known Limitations (Verified, Not Assumed)

These were checked by actually testing package availability and running SQLAlchemy locally — not assumed:

- **No `go.sum` is committed.** The Dockerfiles run `go mod tidy && go mod download` at build time instead of `go mod download` against a pre-committed `go.sum`. This requires network access during `docker build`. If the build fails on dependency resolution, run `cd hitl-go && go mod tidy` locally first, commit the resulting `go.sum`, then you can revert the Dockerfiles to the faster `COPY go.mod go.sum ./` + `go mod download` pattern.
- **`Dockerfile.server` and `Dockerfile.gateway` use BuildKit cache mounts** (`--mount=type=cache,target=/go/pkg/mod`) so both images share the same downloaded Go modules instead of each re-downloading all 7 dependencies independently. Requires BuildKit, which is the default build driver for `docker compose build` on modern Docker — no extra flags needed. If you ever see "unknown flag: --mount" errors, your Docker version predates BuildKit-by-default; run `DOCKER_BUILDKIT=1 docker compose build` explicitly or upgrade Docker.
- **`DATABASE_URL` scheme intentionally differs** between `hitl-server` (`postgres://`) and `celery-worker` (`postgresql://`). Verified that SQLAlchemy 2.x raises `NoSuchModuleError` on bare `postgres://`, while pgx's own documentation uses `postgres://` in its canonical example. Do not "fix" these to match each other.
- **`GROQ_API` / `ANTHROPIC_API_KEY`** are wired into `celery-worker`'s environment but `services/extractor.py` (the file that would read them) is not yet part of this project. They're forward-declared, currently unused.
- **No `package-lock.json`** exists in `frontend/`, so `npm install` inside the Docker build is not pinned to exact versions. Run `npm install` locally once and commit the lockfile if reproducible builds matter.

## Common Issues

| Problem | Fix |
|---|---|
| `gateway` keeps restarting | `hitl-server` not healthy yet — check `docker compose logs hitl-server` |
| Celery worker can't reach Postgres | Confirm `DATABASE_URL` uses service name `postgres`, not `localhost` |
| Frontend shows network errors | Confirm `VITE_GATEWAY_URL=http://gateway:9000` is set in the frontend service env |
| Mailer sends nothing | `export/email_config.json` still has placeholder values |
| Port already in use | Another process (or the non-Docker `./start.sh`) is using 8000/9000/5432/6379 — stop it first |
| Changes to Go code not reflected | Run `docker compose up --build` again — Go binaries are compiled at image build time, no live-reload |
