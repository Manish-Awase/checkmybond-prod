-- migrations/0003_coupons.sql
--
-- Coupon service: generation (admin) + redemption (user).
--
-- Run once, against whichever Postgres the app points at:
--   psql "$DATABASE_URL" -f migrations/0003_coupons.sql
--
-- Safe to re-run — every statement is guarded with IF NOT EXISTS.
--
-- DESIGN NOTES
--
-- `kind` decides what a coupon actually does, and the three kinds behave
-- differently enough that they're worth spelling out:
--
--   'percent' — percentage off a plan purchase. `value` is 0–100.
--               `max_discount` optionally caps the absolute discount so a
--               100%-off bug on the Annual plan can't cost $500.
--   'flat'    — fixed currency amount off a plan purchase. `value` is in
--               `currency` units (dollars, not cents — matches
--               plan_transactions.total_amount, which is also a float
--               dollar amount in routers/payment.go).
--   'tokens'  — grants field-credits directly to users.token_balance with
--               no payment involved at all. `value` is a whole number of
--               fields. This is the kind you hand out at a conference or
--               to a support case; it never touches the payment gateway,
--               for the same reason ClaimFreeHandler doesn't (Stripe and
--               Razorpay both reject a non-positive Amount).
--
-- redeemed_count is denormalized onto the coupon row rather than counted
-- from coupon_redemptions on every check. That's what makes the
-- max_redemptions limit enforceable atomically: the UPDATE ... WHERE
-- redeemed_count < max_redemptions in internal/coupon/store.go is
-- evaluated by Postgres against the current row, so two users racing to
-- claim the last redemption of a coupon can't both win. A COUNT(*) from
-- Go would be a read-then-write and could double-spend.

CREATE TABLE IF NOT EXISTS coupons (
    code                     VARCHAR(64)  PRIMARY KEY,   -- always stored UPPERCASE; see NormalizeCode
    kind                     VARCHAR(16)  NOT NULL,      -- 'percent' | 'flat' | 'tokens'
    value                    NUMERIC(12,2) NOT NULL,     -- percent 0-100 | currency amount | whole fields
    currency                 VARCHAR(8)   NOT NULL DEFAULT 'USD',

    -- Empty/NULL means "valid on every plan". Otherwise a comma-separated
    -- list of plan ids matching routers/payment.go's `plans` map keys
    -- (free, per-field, per-3-field, monthly, annual). Stored as text
    -- rather than a Postgres array so the MySQL planstore path could use
    -- the same schema shape if this ever follows plan_transactions there.
    applies_to_plans         TEXT         NULL,

    min_amount               NUMERIC(12,2) NOT NULL DEFAULT 0,  -- order must be at least this much
    max_discount             NUMERIC(12,2) NULL,                -- cap for 'percent'; NULL = uncapped

    max_redemptions          INTEGER      NULL,          -- NULL = unlimited total redemptions
    max_redemptions_per_user INTEGER      NOT NULL DEFAULT 1,
    redeemed_count           INTEGER      NOT NULL DEFAULT 0,

    starts_at                TIMESTAMP    NOT NULL DEFAULT now(),
    expires_at               TIMESTAMP    NULL,          -- NULL = never expires
    is_active                BOOLEAN      NOT NULL DEFAULT true,

    -- Bulk generation groups codes under one batch_id so an admin can
    -- list/disable a whole campaign at once without knowing every code.
    batch_id                 VARCHAR(64)  NULL,
    campaign                 VARCHAR(128) NULL,
    notes                    TEXT         NULL,

    created_by               VARCHAR      NOT NULL,
    created_at               TIMESTAMP    NOT NULL DEFAULT now(),
    updated_at               TIMESTAMP    NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_coupons_batch_id  ON coupons(batch_id);
CREATE INDEX IF NOT EXISTS idx_coupons_campaign  ON coupons(campaign);
CREATE INDEX IF NOT EXISTS idx_coupons_active    ON coupons(is_active, expires_at);

-- One row per successful redemption. transaction_id is NULL for 'tokens'
-- coupons (no payment happened) and set to plan_transactions.id for
-- 'percent'/'flat' coupons applied at checkout.
CREATE TABLE IF NOT EXISTS coupon_redemptions (
    id              VARCHAR      PRIMARY KEY,
    code            VARCHAR(64)  NOT NULL REFERENCES coupons(code) ON DELETE CASCADE,
    user_id         VARCHAR      NOT NULL,
    username        VARCHAR(64)  NULL,    -- denormalized, survives account deletion
    email           VARCHAR(255) NULL,
    transaction_id  VARCHAR      NULL,    -- plan_transactions.id, NULL for token grants
    discount_amount NUMERIC(12,2) NOT NULL DEFAULT 0,
    tokens_granted  INTEGER      NOT NULL DEFAULT 0,
    original_amount NUMERIC(12,2) NOT NULL DEFAULT 0,
    final_amount    NUMERIC(12,2) NOT NULL DEFAULT 0,
    currency        VARCHAR(8)   NOT NULL DEFAULT 'USD',
    redeemed_at     TIMESTAMP    NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_coupon_redemptions_code    ON coupon_redemptions(code);
CREATE INDEX IF NOT EXISTS idx_coupon_redemptions_user    ON coupon_redemptions(user_id);
CREATE INDEX IF NOT EXISTS idx_coupon_redemptions_code_user ON coupon_redemptions(code, user_id);
CREATE INDEX IF NOT EXISTS idx_coupon_redemptions_at      ON coupon_redemptions(redeemed_at);

-- plan_transactions gets two nullable columns so a discounted purchase
-- records what it would have cost and which coupon changed that. Added
-- with IF NOT EXISTS because internal/planstore/{postgres,mysql}.go
-- creates this table itself via EnsureSchema — this migration only adds
-- to it, it does not own it.
ALTER TABLE plan_transactions ADD COLUMN IF NOT EXISTS coupon_code     VARCHAR(64)   NULL;
ALTER TABLE plan_transactions ADD COLUMN IF NOT EXISTS discount_amount NUMERIC(12,2) NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_plan_transactions_coupon ON plan_transactions(coupon_code);
