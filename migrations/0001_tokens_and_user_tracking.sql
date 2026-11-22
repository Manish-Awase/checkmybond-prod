-- migrations/0001_tokens_and_user_tracking.sql
--
-- Adds: token/field-credit balance, logout tracking, soft-delete tracking,
-- user region, and a feedback/complaints table.
--
-- WHY THIS FILE EXISTS: backend/models/db.py uses SQLAlchemy's
-- Base.metadata.create_all(), which only creates tables that don't exist
-- yet — it never alters an existing table's columns. That's fine for a
-- brand-new deployment (the updated User model in db.py already has these
-- columns baked in), but if you've already run this project before and
-- have a live `users` table, you MUST run this migration by hand or the
-- new columns simply won't exist and every query touching them will fail.
--
-- Run once, against whichever Postgres the app points at:
--   psql "$DATABASE_URL" -f migrations/0001_tokens_and_user_tracking.sql
--
-- Safe to re-run — every statement is guarded with IF NOT EXISTS.

ALTER TABLE users ADD COLUMN IF NOT EXISTS last_logout   TIMESTAMP NULL;
ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted_at    TIMESTAMP NULL;
ALTER TABLE users ADD COLUMN IF NOT EXISTS region        VARCHAR(128) NULL;
ALTER TABLE users ADD COLUMN IF NOT EXISTS token_balance INTEGER NOT NULL DEFAULT 0;

-- Feedback / complaints — submitted either standalone (from a "Feedback"
-- entry point somewhere in the app) or attached to an account-deletion
-- flow (kind = 'account_deletion'), where user_id may reference an
-- account that's since been soft-deleted, so no FK constraint against
-- users(id) — it would block the deletion.
CREATE TABLE IF NOT EXISTS user_feedback (
    id         VARCHAR PRIMARY KEY,
    user_id    VARCHAR NOT NULL,
    username   VARCHAR(64),   -- denormalized snapshot — survives account deletion
    kind       VARCHAR(32) NOT NULL DEFAULT 'feedback', -- 'feedback' | 'complaint' | 'account_deletion'
    message    TEXT NOT NULL,
    rating     INTEGER NULL,  -- optional 1-5, mainly for account_deletion ("why are you leaving")
    created_at TIMESTAMP NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_user_feedback_user_id ON user_feedback(user_id);
CREATE INDEX IF NOT EXISTS idx_user_feedback_kind    ON user_feedback(kind);
