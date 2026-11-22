-- usage_queries.sql  --  run with:  psql "$DATABASE_URL" -f usage_queries.sql
--
-- Three levels of "user", from weakest to strongest signal:
--   1. SIGNED UP   users.created_at
--   2. LOGGED IN   login_events        (needs migration 0008; history starts
--                                       the day you deploy it)
--   3. USED IT     documents           (uploaded a PDF; works from day one,
--                                       uses data you already have)
--
-- Level 3 is the one that answers "registered users who actually used the
-- product". All queries ignore soft-deleted accounts. To also exclude admin
-- / test accounts, uncomment the `AND u.role <> 'admin'` lines.

-- ── 1. Funnel: of everyone registered, how many got how far? ────────────────
SELECT
  count(*)                                                     AS registered,
  count(*) FILTER (WHERE u.last_login IS NOT NULL)             AS ever_logged_in,
  count(*) FILTER (WHERE EXISTS (
        SELECT 1 FROM documents d WHERE d.user_id = u.id))     AS ever_uploaded
FROM users u
WHERE u.deleted_at IS NULL
  -- AND u.role <> 'admin'
;

-- ── 2. New signups per day (change 'day' to 'week' or 'month') ──────────────
SELECT date_trunc('day', created_at)::date AS period, count(*) AS signups
FROM users
WHERE deleted_at IS NULL
GROUP BY 1 ORDER BY 1 DESC LIMIT 60;

-- ── 3. Active users by REAL USAGE (uploaded >= 1 document in the period) ────
--    Works immediately on existing data.
SELECT date_trunc('day', d.created_at)::date AS period,
       count(DISTINCT d.user_id)             AS active_users,
       count(*)                              AS documents
FROM documents d
JOIN users u ON u.id = d.user_id AND u.deleted_at IS NULL
-- WHERE u.role <> 'admin'
GROUP BY 1 ORDER BY 1 DESC LIMIT 60;      -- 'week' / 'month' for the others

-- ── 4. DAU / WAU / MAU right now, by real usage ─────────────────────────────
SELECT
  count(DISTINCT d.user_id) FILTER (WHERE d.created_at > now() - interval '1 day')   AS dau,
  count(DISTINCT d.user_id) FILTER (WHERE d.created_at > now() - interval '7 days')  AS wau,
  count(DISTINCT d.user_id) FILTER (WHERE d.created_at > now() - interval '30 days') AS mau
FROM documents d
JOIN users u ON u.id = d.user_id AND u.deleted_at IS NULL;

-- ── 5. Active users by LOGIN (needs migration 0008) ─────────────────────────
SELECT date_trunc('day', logged_in_at)::date AS period,
       count(DISTINCT user_id)               AS users_logged_in,
       count(*)                              AS logins
FROM login_events
GROUP BY 1 ORDER BY 1 DESC LIMIT 60;      -- 'week' / 'month' for the others

-- ── 6. Registered but never uploaded anything (people to nudge) ─────────────
SELECT u.email, u.created_at, u.last_login
FROM users u
WHERE u.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM documents d WHERE d.user_id = u.id)
ORDER BY u.created_at DESC;

-- ── 7. Most active users, last 30 days ──────────────────────────────────────
SELECT u.email, count(*) AS documents, max(d.created_at) AS last_upload
FROM documents d
JOIN users u ON u.id = d.user_id AND u.deleted_at IS NULL
WHERE d.created_at > now() - interval '30 days'
GROUP BY u.email ORDER BY documents DESC LIMIT 20;

-- ── 8. Retention: of users who signed up each month, % who uploaded within
--       their first 7 days ───────────────────────────────────────────────────
SELECT date_trunc('month', u.created_at)::date AS signup_month,
       count(*)                                AS signups,
       count(*) FILTER (WHERE EXISTS (
            SELECT 1 FROM documents d
            WHERE d.user_id = u.id
              AND d.created_at <= u.created_at + interval '7 days')) AS used_in_first_week
FROM users u
WHERE u.deleted_at IS NULL
GROUP BY 1 ORDER BY 1 DESC;
