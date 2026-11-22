-- migrations/0008_login_events.sql
--
-- Keeps one row per successful login, so you can chart daily/weekly/monthly
-- active users historically. users.last_login is overwritten on every login
-- and cannot answer "how many people logged in last Tuesday".
--
-- Run once:  psql "$DATABASE_URL" -f migrations/0008_login_events.sql
-- Safe to re-run (IF NOT EXISTS).
--
-- No FK to users(id) on purpose: a soft-deleted/removed account should not
-- block anything, and the history stays intact.

CREATE TABLE IF NOT EXISTS login_events (
    id           BIGSERIAL PRIMARY KEY,
    user_id      VARCHAR   NOT NULL,
    logged_in_at TIMESTAMP NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_login_events_at      ON login_events (logged_in_at);
CREATE INDEX IF NOT EXISTS idx_login_events_user_at ON login_events (user_id, logged_in_at);
