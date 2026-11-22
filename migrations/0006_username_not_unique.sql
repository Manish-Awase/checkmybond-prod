-- migrations/0006_username_not_unique.sql
--
-- Email is the ONLY unique identity on a user. The username becomes a
-- plain display name that any number of accounts may share.
--
-- Run once, against whichever Postgres the app points at:
--   psql -v ON_ERROR_STOP=1 "$DATABASE_URL" -f migrations/0006_username_not_unique.sql
--
-- Safe to re-run. Runs in one transaction: if anything fails -- notably
-- the duplicate-email check below -- NOTHING is changed, including the
-- username constraint, so the database is never left half-migrated.
--
-- Requires PostgreSQL 11+.
--
-- WHAT IT DOES
--
--  1. Drops every UNIQUE constraint or index whose only column is
--     "username" -- found by what it covers, not by name, because a
--     database created by hand or by an older model can have it as a
--     unique INDEX (e.g. ix_users_username) instead of the constraint
--     (uq_users_username) the current model implies. Plain, partial
--     (WHERE ...) and expression (lower(username)) forms are all caught.
--     A composite unique that merely INCLUDES username (say, username +
--     org) is not a single-column rule, so it is left alone and reported
--     with a WARNING for you to decide on.
--
--  2. Refuses to continue if two accounts already share an email address
--     (ignoring case). Merging or removing accounts is not something a
--     migration should decide, so it stops and tells you how to find them.
--
--  3. Adds a unique index on lower(email), so "A@x.com" and "a@x.com" are
--     one address. The application matches emails case-insensitively
--     (hitl-go GetUserByEmail), and this is what makes that safe: at most
--     one row can ever match. The existing exact-match constraint
--     uq_users_email, where present, is untouched.
--
-- THERE IS NO PHONE NUMBER on users, so nothing here concerns one. If one
-- is ever added as a second way to identify an account, it needs its own
-- nullable column with a partial unique index (UNIQUE ... WHERE phone IS
-- NOT NULL) so accounts without one don't collide with each other.

BEGIN;

-- ── 1. Username stops being unique ─────────────────────────────────────────
DO $$
DECLARE
    r       record;
    dropped text[] := '{}';
BEGIN
    -- (a) UNIQUE constraints on username alone.
    FOR r IN
        SELECT c.conname
          FROM pg_constraint c
          JOIN pg_class t     ON t.oid = c.conrelid
          JOIN pg_namespace n ON n.oid = t.relnamespace
         WHERE n.nspname = current_schema()
           AND t.relname = 'users'
           AND c.contype = 'u'
           AND array_length(c.conkey, 1) = 1
           AND (SELECT a.attname FROM pg_attribute a
                 WHERE a.attrelid = c.conrelid AND a.attnum = c.conkey[1]) = 'username'
    LOOP
        EXECUTE format('ALTER TABLE users DROP CONSTRAINT %I', r.conname);
        dropped := dropped || ('constraint ' || r.conname);
    END LOOP;

    -- (b) Stand-alone UNIQUE indexes on username alone. (An index that
    --     backed a constraint went with it above.) Covers a plain column,
    --     a partial index, and an expression index such as lower(username).
    FOR r IN
        SELECT ni.nspname AS sch, i.relname AS idx
          FROM pg_index x
          JOIN pg_class i      ON i.oid = x.indexrelid
          JOIN pg_namespace ni ON ni.oid = i.relnamespace
          JOIN pg_class t      ON t.oid = x.indrelid
          JOIN pg_namespace n  ON n.oid = t.relnamespace
         WHERE n.nspname = current_schema()
           AND t.relname = 'users'
           AND x.indisunique
           AND NOT x.indisprimary
           AND x.indnkeyatts = 1
           AND (
                 (x.indkey[0] <> 0 AND
                  (SELECT a.attname FROM pg_attribute a
                    WHERE a.attrelid = t.oid AND a.attnum = x.indkey[0]) = 'username')
              OR (x.indkey[0] = 0 AND
                  pg_get_expr(x.indexprs, x.indrelid) ~* '\musername\M')
               )
    LOOP
        EXECUTE format('DROP INDEX %I.%I', r.sch, r.idx);
        dropped := dropped || ('index ' || r.idx);
    END LOOP;

    RAISE NOTICE 'username uniqueness removed: %',
        COALESCE(NULLIF(array_to_string(dropped, ', '), ''), '(nothing - it was already not unique)');
END
$$;

-- (c) Anything unique that still involves username is a composite rule we
--     deliberately did not touch. Say so rather than leave it unnoticed.
DO $$
DECLARE
    r record;
BEGIN
    FOR r IN
        SELECT pg_get_indexdef(i.oid) AS def
          FROM pg_index x
          JOIN pg_class i     ON i.oid = x.indexrelid
          JOIN pg_class t     ON t.oid = x.indrelid
          JOIN pg_namespace n ON n.oid = t.relnamespace
         WHERE n.nspname = current_schema()
           AND t.relname = 'users'
           AND x.indisunique
           AND NOT x.indisprimary
           AND pg_get_indexdef(i.oid) ~* '\musername\M'
    LOOP
        RAISE WARNING 'a unique rule involving username remains (composite, left alone on purpose - drop it yourself if it should go): %', r.def;
    END LOOP;
END
$$;

-- ── 2. Email must already be unambiguous ───────────────────────────────────
DO $$
DECLARE
    n integer;
BEGIN
    SELECT count(*) INTO n
      FROM (SELECT 1 FROM users GROUP BY lower(email) HAVING count(*) > 1) d;
    IF n > 0 THEN
        RAISE EXCEPTION
            'cannot make email the unique identity: % address(es) are used by more than one account (ignoring case). Find them with:  SELECT lower(email), array_agg(id) FROM users GROUP BY 1 HAVING count(*) > 1;  then merge, rename or remove the extras and re-run this migration.', n
            USING ERRCODE = 'unique_violation';
    END IF;
END
$$;

-- ── 3. Email is unique, ignoring case ──────────────────────────────────────
CREATE UNIQUE INDEX IF NOT EXISTS ux_users_email_lower ON users (lower(email));

COMMIT;
