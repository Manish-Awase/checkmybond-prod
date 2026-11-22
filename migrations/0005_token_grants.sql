-- migrations/0005_token_grants.sql
--
-- Dated credit grants, so purchased fields can expire.
--
-- Before this, users.token_balance was a single running number with no
-- record of WHICH purchase a field came from or WHEN it should lapse --
-- see the comment that used to be on routers/payment.go's plans map.
-- "Monthly"/"Annual" plans are one-time bundle purchases (no
-- subscriptions, no auto-renewal, no auto-charge -- see
-- COUPON_AND_ALERTING.md), but their fields DO expire: 1 month or 1 year
-- after the purchase, respectively. Every other grant (free, per-field,
-- bundle, coupon) never expires.
--
-- token_grants is the ledger backing that: one row per credit event.
-- users.token_balance remains the fast, cached total everything already
-- reads -- this table exists so that total can be dated, swept, and
-- shown to the user, not to replace the column.
CREATE TABLE IF NOT EXISTS token_grants (
    id               TEXT PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES users(id),
    transaction_id   TEXT,               -- plan_transactions.id, when this came from a purchase
    plan_id          TEXT NOT NULL,      -- 'free' | 'per-field' | 'per-3-field' | 'monthly' | 'annual' | 'coupon'
    source           TEXT NOT NULL,      -- human-readable: 'purchase' | 'free_claim' | 'coupon'
    fields_granted   INTEGER NOT NULL CHECK (fields_granted > 0),
    fields_remaining INTEGER NOT NULL CHECK (fields_remaining >= 0),
    granted_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ,        -- NULL = never expires
    expired_at       TIMESTAMPTZ,        -- set once by the sweep job when reclaimed
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_token_grants_user
    ON token_grants (user_id);

-- The sweep job's own query pattern: unexpired grants with an expiry
-- date that has passed. Partial index keeps it cheap even with a large
-- history of long-since-expired or never-expiring grants.
CREATE INDEX IF NOT EXISTS idx_token_grants_pending_expiry
    ON token_grants (expires_at)
    WHERE expires_at IS NOT NULL AND expired_at IS NULL;

-- DeductTokens' consumption order: soonest-expiring non-exhausted grant
-- first (NULLs -- never-expiring grants -- last), so a user's use-it-
-- or-lose-it credits get spent before the ones that never lapse.
CREATE INDEX IF NOT EXISTS idx_token_grants_consumption_order
    ON token_grants (user_id, expires_at NULLS LAST, granted_at)
    WHERE fields_remaining > 0 AND expired_at IS NULL;
