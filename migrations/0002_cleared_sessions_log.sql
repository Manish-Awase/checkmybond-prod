-- migrations/0002_cleared_sessions_log.sql
--
-- One small summary table: how many files had been processed by the time
-- someone clicked "Start Fresh" and abandoned the rest. Nothing else —
-- Start Fresh's actual delete behavior is correct as-is; this just logs a
-- count for later analysis before that delete runs.
--
-- Run once:
--   psql "postgresql://postgres:postgres@postgres:5432/hitl" -f migrations/0002_cleared_sessions_log.sql
--
-- Safe to re-run — CREATE TABLE IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS cleared_sessions_log (
    id             SERIAL PRIMARY KEY,
    session_id     VARCHAR   NOT NULL,
    user_id        VARCHAR   NULL,
    total_files    INTEGER   NOT NULL DEFAULT 0,
    queued_count   INTEGER   NOT NULL DEFAULT 0,  -- successfully processed before clearing
    skipped_count  INTEGER   NOT NULL DEFAULT 0,
    failed_count   INTEGER   NOT NULL DEFAULT 0,
    session_created_at TIMESTAMP NULL,
    cleared_at     TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE password_resets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP DEFAULT now()
);