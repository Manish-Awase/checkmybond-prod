# mailer/Dockerfile
# Standalone Go mailer binary.
# Run as a one-shot container triggered by host cron or docker-compose
# 'run --rm mailer', not as a long-running service.

FROM golang:1.22-alpine AS builder
WORKDIR /build

COPY go.mod ./
RUN go mod download 2>/dev/null || true

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /build/mailer .

# ── Final stage ────────────────────────────────────────────────────────────────
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app

COPY --from=builder /build/mailer .

ENTRYPOINT ["./mailer"]
