-- database/verify.sql
--
-- Did the database come up right?   psql -d checkmybond -f database/verify.sql
--
-- Prints one line per check, MISSING ones first, then a one-line verdict.
-- Read-only: it changes nothing.

WITH checks(what, ok) AS (VALUES
    ('table users', to_regclass('public.users') IS NOT NULL),
    ('table organizations', to_regclass('public.organizations') IS NOT NULL),
    ('table password_resets', to_regclass('public.password_resets') IS NOT NULL),
    ('table user_feedback', to_regclass('public.user_feedback') IS NOT NULL),
    ('table login_events', to_regclass('public.login_events') IS NOT NULL),
    ('table documents', to_regclass('public.documents') IS NOT NULL),
    ('table schema_registry', to_regclass('public.schema_registry') IS NOT NULL),
    ('table extraction_jobs', to_regclass('public.extraction_jobs') IS NOT NULL),
    ('table field_library', to_regclass('public.field_library') IS NOT NULL),
    ('table template_definitions', to_regclass('public.template_definitions') IS NOT NULL),
    ('table bond_sessions', to_regclass('public.bond_sessions') IS NOT NULL),
    ('table extractions', to_regclass('public.extractions') IS NOT NULL),
    ('table corrections', to_regclass('public.corrections') IS NOT NULL),
    ('table audit_log', to_regclass('public.audit_log') IS NOT NULL),
    ('table chunks', to_regclass('public.chunks') IS NOT NULL),
    ('table prompt_versions', to_regclass('public.prompt_versions') IS NOT NULL),
    ('table upload_sessions', to_regclass('public.upload_sessions') IS NOT NULL),
    ('table upload_session_files', to_regclass('public.upload_session_files') IS NOT NULL),
    ('table cleared_sessions_log', to_regclass('public.cleared_sessions_log') IS NOT NULL),
    ('table plan_transactions', to_regclass('public.plan_transactions') IS NOT NULL),
    ('table token_grants', to_regclass('public.token_grants') IS NOT NULL),
    ('table coupons', to_regclass('public.coupons') IS NOT NULL),
    ('table coupon_redemptions', to_regclass('public.coupon_redemptions') IS NOT NULL),
    ('table alert_rules', to_regclass('public.alert_rules') IS NOT NULL),
    ('table alert_recipients', to_regclass('public.alert_recipients') IS NOT NULL),
    ('table alert_events', to_regclass('public.alert_events') IS NOT NULL),
    ('email is unique, ignoring case',
        EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND tablename='users'
                  AND indexname='ux_users_email_lower' AND indexdef LIKE 'CREATE UNIQUE%')),
    ('username is NOT unique (it is only a display name)',
        NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND tablename='users'
                      AND indexdef LIKE 'CREATE UNIQUE%' AND indexdef ~* '\musername\M')),
    ('plan_transactions can record refunds',
        EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public'
                  AND table_name='plan_transactions' AND column_name='refunded_amount')),
    ('plan_transactions can record coupons',
        EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public'
                  AND table_name='plan_transactions' AND column_name='coupon_code')),
    ('token_grants can expire credits',
        EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public'
                  AND table_name='token_grants' AND column_name='expires_at')),
    ('the 17 starting alert rules are loaded',
        CASE WHEN to_regclass('public.alert_rules') IS NULL THEN false
             ELSE (xpath('/row/c/text()',
                         query_to_xml('select count(*) as c from public.alert_rules', false, true, '')))[1]::text::int >= 17
        END)
)
SELECT CASE WHEN ok THEN 'ok     ' ELSE 'MISSING' END AS status, what
  FROM checks
 ORDER BY ok, what;

WITH checks(ok) AS (VALUES
    (to_regclass('public.users') IS NOT NULL),
    (to_regclass('public.organizations') IS NOT NULL),
    (to_regclass('public.password_resets') IS NOT NULL),
    (to_regclass('public.user_feedback') IS NOT NULL),
    (to_regclass('public.login_events') IS NOT NULL),
    (to_regclass('public.documents') IS NOT NULL),
    (to_regclass('public.schema_registry') IS NOT NULL),
    (to_regclass('public.extraction_jobs') IS NOT NULL),
    (to_regclass('public.field_library') IS NOT NULL),
    (to_regclass('public.template_definitions') IS NOT NULL),
    (to_regclass('public.bond_sessions') IS NOT NULL),
    (to_regclass('public.extractions') IS NOT NULL),
    (to_regclass('public.corrections') IS NOT NULL),
    (to_regclass('public.audit_log') IS NOT NULL),
    (to_regclass('public.chunks') IS NOT NULL),
    (to_regclass('public.prompt_versions') IS NOT NULL),
    (to_regclass('public.upload_sessions') IS NOT NULL),
    (to_regclass('public.upload_session_files') IS NOT NULL),
    (to_regclass('public.cleared_sessions_log') IS NOT NULL),
    (to_regclass('public.plan_transactions') IS NOT NULL),
    (to_regclass('public.token_grants') IS NOT NULL),
    (to_regclass('public.coupons') IS NOT NULL),
    (to_regclass('public.coupon_redemptions') IS NOT NULL),
    (to_regclass('public.alert_rules') IS NOT NULL),
    (to_regclass('public.alert_recipients') IS NOT NULL),
    (to_regclass('public.alert_events') IS NOT NULL)
)
SELECT CASE WHEN count(*) FILTER (WHERE NOT ok) = 0
            THEN 'ALL ' || count(*) || ' TABLES PRESENT'
            ELSE count(*) FILTER (WHERE NOT ok) || ' TABLE(S) MISSING -- see the list above' END AS verdict
  FROM checks;
