# Nginx Setup

Nginx sits in front of the Go gateway and adds the features the project was missing:

| Feature | Before Nginx | With Nginx |
|---|---|---|
| Request forwarding | ✅ Go gateway | ✅ Nginx → Go gateway |
| Header injection | ✅ Go gateway | ✅ Go gateway (unchanged) |
| TLS/HTTPS termination | ❌ HTTP only | ✅ Port 443, TLS 1.2/1.3 |
| Rate limiting | ❌ | ✅ Per-IP, per-endpoint |
| Static file serving | ❌ Vite dev server | ✅ Nginx serves built files |
| Load balancing | ❌ Single backend | ✅ Multiple gateway replicas |
| Gzip compression | ❌ | ✅ All JSON/JS/CSS responses |

---

## Traffic Flow

```
Browser
  │
  ▼  HTTPS :443
Nginx                          ← NEW — TLS, rate limit, gzip, static files
  ├── /              → static  ← React build files served directly
  ├── /api/*         → proxy   → Go Gateway :9000
  └── /health        → proxy   → Go Gateway :9000
                                    │
                                    ▼
                               Go API Server :8000
```

---

## Before You Start

### Local development — self-signed certificate

```bash
bash nginx/ssl/generate-self-signed.sh
```

This creates `nginx/ssl/cert.pem` and `nginx/ssl/key.pem`.
Your browser will show a warning — click **Advanced → Proceed to localhost**.

### Production — Let's Encrypt

```bash
# 1. Point your domain's DNS A record to this server's IP

# 2. Start Nginx on HTTP only first (comment out the HTTPS server block in nginx.conf)
docker compose up nginx

# 3. Get the certificate
docker compose run --rm certbot certonly \
  --webroot --webroot-path /var/www/certbot \
  -d yourdomain.com

# 4. Update nginx.conf ssl_certificate paths to:
#    /etc/letsencrypt/live/yourdomain.com/fullchain.pem
#    /etc/letsencrypt/live/yourdomain.com/privkey.pem

# 5. Restart Nginx
docker compose restart nginx
```

---

## Starting the Stack

```bash
# Generate SSL cert first
bash nginx/ssl/generate-self-signed.sh

# Start everything
docker compose up --build

# Access the app
open https://localhost
```

`http://localhost` redirects automatically to `https://localhost`.

---

## Rate Limiting

Three zones are configured in `nginx.conf`:

| Zone | Endpoint | Limit | Burst |
|---|---|---|---|
| `auth` | `/api/auth/*` | 10 req/min per IP | 5 |
| `upload` | `/api/documents/upload*` | 5 req/min per IP | 3 |
| `api` | All other `/api/*` | 60 req/sec per IP | 20 |

When a limit is exceeded, Nginx returns:
```json
{"detail": "Too many requests — slow down"}
```
with HTTP status `429`.

---

## Load Balancing

To run multiple Go gateway replicas, add them to `nginx.conf`:

```nginx
upstream hitl_gateway {
    server  gateway:9000;
    server  gateway2:9000;    # add replica
    server  gateway3:9000;    # add replica
    least_conn;               # route to least-busy
    keepalive  32;
}
```

And add the replicas in `docker-compose.yml`:

```yaml
gateway2:
  build:
    context:    ./hitl-go
    dockerfile: Dockerfile.gateway
  environment:
    BACKEND_URL:  http://hitl-server:8000
    JWT_SECRET:   ${JWT_SECRET}
    REDIS_HOST:   redis
    GATEWAY_PORT: "9000"
  networks: [hitl-net]
```

Note: `hitl-server` itself is single-instance — it's stateless (all state
is in Postgres and Redis) so it can also be scaled the same way if needed.

---

## Static File Serving (Production)

In dev mode, Nginx proxies to the Vite dev server at `frontend:5173`.
In production, build the frontend first and let Nginx serve the files directly:

```bash
# Build frontend image using production Dockerfile
docker compose build frontend --build-arg DOCKERFILE=Dockerfile.prod

# Or build manually
cd frontend
npm run build    # outputs to frontend/dist/
```

Then in `docker-compose.yml`, update the nginx volumes to mount `frontend/dist`:

```yaml
nginx:
  volumes:
    - ./nginx/nginx.conf:/etc/nginx/nginx.conf:ro
    - ./nginx/ssl:/etc/nginx/ssl:ro
    - ./frontend/dist:/usr/share/nginx/html:ro    # serve built files
```

And in `nginx.conf`, uncomment the static files location block and
comment out the Vite proxy location block.

---

## Gzip

Enabled for all responses larger than 1KB:
- `application/json` — API responses
- `application/javascript` — React bundle
- `text/css` — Stylesheets
- `application/wasm` — WebAssembly (if used)
- `font/woff`, `font/woff2` — Web fonts

Compression level 6 — good balance of CPU cost vs size reduction.
Typical API JSON responses compress 60-80%.

---

## Files

```
nginx/
├── Dockerfile                   Nginx image — copies nginx.conf
├── nginx.conf                   Full config — TLS, rate limiting, gzip, upstreams
├── ssl/
│   ├── generate-self-signed.sh  Quick cert for local dev
│   ├── cert.pem                 (generated — not in git)
│   └── key.pem                  (generated — not in git)
└── certbot/                     Let's Encrypt ACME challenge dir (empty initially)

frontend/
└── Dockerfile.prod              Production build — Node builds, Nginx serves
```
