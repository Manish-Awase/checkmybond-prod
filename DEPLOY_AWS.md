# Deploying Project11 to AWS (ap-south-1)

This guide takes Project11 from local Docker Compose (Docker-managed
Postgres/Redis) to a production AWS setup: **RDS PostgreSQL** +
**ElastiCache Redis** + **EC2** (Docker Compose for everything else).

No Go, Python, or TypeScript source changes are required. Only
`docker-compose.aws.yml` and `.env` differ from the local setup.

---

## 0. What's changing vs. local

| Component | Local (`docker-compose.yml`) | AWS (`docker-compose.aws.yml`) |
|---|---|---|
| Postgres | `postgres:` container | AWS RDS PostgreSQL 16 |
| Redis | `redis:` container | AWS ElastiCache Redis (cluster mode **disabled**) |
| Qdrant | container | container (unchanged — stays on EC2) |
| Go server / gateway, Celery worker, Nginx, frontend | containers | same containers, same images, just pointed at RDS/ElastiCache via env vars |

---

## 1. Create the RDS PostgreSQL instance

1. AWS Console → RDS → **Create database**, region **ap-south-1 (Mumbai)**.
2. Engine: **PostgreSQL**, version **16.x** (required — matches project's tested version).
3. Templates: Production (or Dev/Test if cost-sensitive — same engine either way).
4. Instance class: `db.t3.medium` minimum (matches the cost model in the handoff doc).
5. Storage: 20 GB gp3 to start, enable storage autoscaling.
6. Settings:
   - DB instance identifier: `project11-db`
   - Master username: `hitl_user`
   - Master password: generate a strong password, save it — this becomes `DB_PASSWORD` / part of `DATABASE_URL`
7. Connectivity:
   - VPC: same VPC you'll launch the EC2 instance in
   - Public access: **No**
   - VPC security group: create new, name it `project11-rds-sg` (rules configured in step 4 below)
8. Additional configuration → Initial database name: `hitl`
9. Create database. Wait for status **Available**, then copy the **endpoint** (looks like `project11-db.xxxxxxxxxxxx.ap-south-1.rds.amazonaws.com`).

---

## 2. Create the ElastiCache Redis cluster

1. AWS Console → ElastiCache → **Redis clusters** → **Create Redis cluster**, region **ap-south-1**.
2. Deployment option: **Design your own cache** → **Cluster mode: Disabled**
   - This is required — the project uses `SELECT 0` (token blacklist) and
     `SELECT 1` (Celery broker) to pick Redis logical databases, and
     cluster mode does not support multiple logical databases.
3. Engine version: Redis 7.x
4. Cluster name: `project11-redis`
5. Node type: `cache.t3.micro` to start (matches the cost model; scale up if the Celery broker gets backed up).
6. Number of replicas: 0 for cost, 1+ if you need failover.
7. Subnet group: same VPC as EC2/RDS.
8. Security: create a new security group, `project11-redis-sg` (rules in step 4).
9. Create. Wait for status **Available**, then copy the **Primary endpoint** (no port, e.g. `project11-redis.xxxxxx.ng.0001.aps1.cache.amazonaws.com`).

---

## 3. Launch the EC2 instance

1. EC2 → **Launch instance**, region **ap-south-1**.
2. AMI: **Ubuntu Server 22.04 LTS**.
3. Instance type: **t3.large minimum** — the Celery worker loads
   `sentence-transformers` embedding models and needs 4GB+ RAM; anything
   smaller than t3.large risks OOM kills under load.
4. Key pair: create/select one you can SSH in with.
5. Network: same VPC as RDS/ElastiCache. Assign a public IP (or use an
   Elastic IP so the address doesn't change on restart).
6. Security group: create `project11-ec2-sg` with inbound rules:
   - `22/tcp` from your IP (SSH)
   - `80/tcp` from `0.0.0.0/0` (HTTP, redirects to HTTPS)
   - `443/tcp` from `0.0.0.0/0` (HTTPS — Nginx is the only public entry point)
   - Do **not** open 8000, 9000, 5173, or 6333 to the internet — those
     are internal-only ports reached via Nginx or within the Docker network.
7. Storage: 30GB+ gp3 (EBS) — holds `export/`, `uploads/`, and the Qdrant
   volume.
8. Launch.

---

## 4. Configure security group rules (RDS / ElastiCache inbound)

Go back to the two security groups created in steps 1 and 2 and add
inbound rules that reference the EC2 security group (not an IP range —
security-group-to-security-group references stay correct even if the
EC2 IP changes):

**`project11-rds-sg`** — inbound:
| Type | Port | Source |
|---|---|---|
| PostgreSQL | 5432 | `project11-ec2-sg` (the EC2 instance's security group) |

**`project11-redis-sg`** — inbound:
| Type | Port | Source |
|---|---|---|
| Custom TCP | 6379 | `project11-ec2-sg` |

**`project11-ec2-sg`** — outbound (usually allow-all by default, but if
you've locked it down, explicitly allow):
| Type | Port | Destination |
|---|---|---|
| PostgreSQL | 5432 | `project11-rds-sg` |
| Custom TCP | 6379 | `project11-redis-sg` |
| HTTPS | 443 | `0.0.0.0/0` (pulling Docker images, Groq/Anthropic API calls) |

---

## 5. Install Docker on EC2

SSH into the instance, then:

```bash
ssh -i your-key.pem ubuntu@<ec2-public-ip>

sudo apt update && sudo apt install -y docker.io docker-compose-plugin
sudo usermod -aG docker ubuntu
newgrp docker

# verify
docker --version
docker compose version
```

---

## 6. Upload the project and configure environment

From your local machine:

```bash
scp -i your-key.pem -r project11-final ubuntu@<ec2-public-ip>:~/project11
```

Back on the EC2 instance:

```bash
cd ~/project11

# TLS cert (self-signed to start; swap for a real cert / Let's Encrypt later)
bash nginx/ssl/generate-self-signed.sh

# Environment
cp .env.aws.example .env
nano .env
```

Fill in `.env` with:
- `DATABASE_URL` — `postgres://hitl_user:<password>@<rds-endpoint>:5432/hitl`
- `DB_USER`, `DB_PASSWORD`, `DB_HOST`, `DB_NAME` — same RDS credentials, split into parts
- `REDIS_HOST` — the ElastiCache primary endpoint (no port)
- `JWT_SECRET` — a real random 256-bit secret (`openssl rand -hex 32`)
- `OWNER_USERNAME` / `OWNER_PASSWORD`
- `CORS_ORIGINS` — your EC2 public DNS or domain, e.g. `https://project11.example.com`
- `GROQ_API`, `ANTHROPIC_API_KEY`

See `.env.aws.example` for the full annotated list.

---

## 7. Deploy

```bash
docker compose -f docker-compose.aws.yml up --build -d
```

This builds and starts: `qdrant`, `hitl-server`, `gateway`, `celery-worker`,
`nginx`, `frontend`. `mailer` stays a manual-trigger profile and does not
start automatically.

Check status:

```bash
docker compose -f docker-compose.aws.yml ps
docker compose -f docker-compose.aws.yml logs -f hitl-server
docker compose -f docker-compose.aws.yml logs -f celery-worker
```

Verify the app is reachable:

```bash
curl -k https://<ec2-public-ip>/health
```

---

## 8. Verify RDS and ElastiCache connectivity from inside the containers

```bash
# Postgres — should list the 11 tables
docker exec hitl-server sh -c 'echo "\dt" | psql "$DATABASE_URL"' 2>/dev/null || \
docker exec hitl-celery-worker python -c "from sqlalchemy import create_engine, text; import os; e=create_engine(os.environ['DATABASE_URL']); print(e.connect())"

# Redis
docker exec hitl-gateway sh -c 'nc -zv $REDIS_HOST 6379'
```

If either fails, re-check the security group rules in step 4 first —
that's the most common cause of connection timeouts here.

---

## 9. Ongoing operations

```bash
# Redeploy after a code change
git pull   # or re-scp the changed files
docker compose -f docker-compose.aws.yml up --build -d

# Tail logs
docker compose -f docker-compose.aws.yml logs -f

# Stop everything
docker compose -f docker-compose.aws.yml down

# Run the mailer manually (it's profile: manual, so it won't autostart)
docker compose -f docker-compose.aws.yml --profile manual run --rm mailer
```

**Backups:** RDS automated backups/snapshots cover Postgres. The Qdrant
volume (`qdrant-data`) lives on the EC2 instance's EBS volume — snapshot
that EBS volume on a schedule if the vector index needs to survive
instance replacement.

---

## 10. Payment gateways (Stripe / Razorpay)

`PAYMENT_GATEWAY` in `.env` controls which one is live:

- `mock` (default) — always succeeds instantly, no keys needed. Safe for staging.
- `stripe` — creates a real Stripe PaymentIntent and collects card details via
  Stripe Elements (`StripeCardForm.tsx`) client-side, confirming the charge
  in the browser. Needs `STRIPE_SECRET_KEY` + `STRIPE_PUBLISHABLE_KEY`.
- `razorpay` — creates a Razorpay Order server-side, opens Razorpay Checkout
  client-side, and verifies the signature via `POST /api/payment/verify`.
  Needs `RAZORPAY_KEY_ID` + `RAZORPAY_KEY_SECRET`.

Both gateways also have a webhook endpoint that catches events your server
might otherwise miss (a delayed confirmation, a refund, a dispute):

| Gateway | Webhook URL | Configure in | Secret env var |
|---|---|---|---|
| Stripe | `https://<your-domain>/api/payment/webhook/stripe` | Dashboard → Developers → Webhooks | `STRIPE_WEBHOOK_SECRET` |
| Razorpay | `https://<your-domain>/api/payment/webhook/razorpay` | Dashboard → Settings → Webhooks | `RAZORPAY_WEBHOOK_SECRET` |

The webhook secrets are **separate** from the API secret keys above —
generate them when you register each webhook URL in that gateway's
dashboard, and set them in `.env` too.

Set the matching keys in `.env` before switching `PAYMENT_GATEWAY` away from
`mock`. Before your first build with either gateway enabled:

```bash
cd hitl-go && go mod tidy        # pulls in stripe-go and razorpay-go, regenerates go.sum
cd ../frontend && npm install    # pulls in @stripe/stripe-js and @stripe/react-stripe-js
```

---

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `hitl-server` can't reach Postgres | RDS security group missing inbound rule from EC2 SG, or wrong `DATABASE_URL` |
| `celery-worker` can't reach Postgres | `DB_HOST`/`DB_USER`/`DB_PASSWORD`/`DB_NAME` mismatch with RDS, or SQLAlchemy got a `postgres://` string instead of `postgresql://` (check it's using the assembled `DATABASE_URL` inside the container, not the top-level `.env` one) |
| Gateway/blacklist or Celery tasks silently not processing | `REDIS_HOST` wrong, or ElastiCache security group missing inbound rule from EC2 SG |
| Token blacklist not working after logout, but broker works (or vice versa) | Cluster mode was left **enabled** on ElastiCache — logical DB switching (`SELECT 0/1`) silently fails in cluster mode; recreate the cluster with cluster mode disabled |
| Celery worker OOM-killed | EC2 instance smaller than t3.large — `sentence-transformers` needs 4GB+ RAM |
| Can reach `:9000` or `:8000` from the internet | Those ports got published in `docker-compose.aws.yml` — they should stay commented out; Nginx (`:443`) is the only intended public entry point |
