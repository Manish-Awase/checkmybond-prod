# database/

One file builds the whole database for a **brand-new install**:
`schema.sql`. No migrations to run, no order to get right, no need to start
the server first.

You need PostgreSQL **13 or newer**. (Redis and Qdrant are separate services
and are not part of this file.)

## Launch it

```bash
createdb checkmybond
psql -v ON_ERROR_STOP=1 -d checkmybond -f database/schema.sql
psql -d checkmybond -f database/verify.sql        # optional: confirms it came up right
```

Then point the app at it with the same `DATABASE_URL` setting you already
use, for example `postgresql://user:password@host:5432/checkmybond`.

`verify.sql` prints one line per check and ends with `ALL 26 TABLES PRESENT`.
It only reads; it changes nothing.

**On a hosted database** (Supabase, Neon, RDS, ...): create an empty database
in the provider's console, open its SQL editor, paste the whole of
`schema.sql` and run it.

**With Docker:** the official Postgres image runs any `.sql` file in
`/docker-entrypoint-initdb.d/` the first time it starts:

```bash
docker run -d --name checkmybond-db \
  -e POSTGRES_PASSWORD=choose-a-password -e POSTGRES_DB=checkmybond \
  -v "$(pwd)/database/schema.sql:/docker-entrypoint-initdb.d/01-schema.sql:ro" \
  -p 5432:5432 postgres:16
```

(That is a standard feature of the image, but I could not run Docker where I
built this, so it is the one path here that I haven't run myself. The `psql`
path above is the tested one.)

## Make your first admin

The file creates **no users** (so there is no default password to forget to
change), and public sign-up only ever makes ordinary analyst accounts. So:

1. Sign up in the app with your own email.
2. Promote that account:
   ```sql
   UPDATE users SET role = 'admin' WHERE lower(email) = lower('you@example.com');
   ```
3. **Log out and back in** — your role is stored in your login token, so it
   only updates when you sign in again.

After that, admins create everyone else's accounts (reviewers, other admins)
from the app's **RegisterEmployee** page (`/registeremployee`, admins only).

## Before you point real users at it

- Set `OWNER_PASSWORD` (and `OWNER_USERNAME`). If it is unset the owner portal
  falls back to a well-known default password.
- Set your payment keys and **webhook** secrets (`STRIPE_*` / `RAZORPAY_*`),
  or refunds and payments can't be recorded.
- Set `METRICS_TOKEN` (or `METRICS_ALLOW_CIDR`) so `/metrics` isn't public.

## It won't run on a database that's already in use

If a table called `users` already exists, `schema.sql` stops immediately with
a clear message and changes nothing. To bring an **existing** database up to
date, run the numbered files in `migrations/` instead.

It also runs as a single transaction: if anything fails part-way, nothing is
left half-built.

## What's in it

26 tables, plus the 17 starting alert rules:

| Area | Tables |
|---|---|
| Accounts | `users`, `organizations`, `password_resets`, `user_feedback`, `login_events` |
| Documents & extraction | `documents`, `schema_registry`, `extraction_jobs`, `field_library`, `template_definitions`, `bond_sessions`, `extractions`, `corrections`, `audit_log`, `chunks`, `prompt_versions`, `upload_sessions`, `upload_session_files`, `cleared_sessions_log` |
| Billing | `plan_transactions`, `token_grants`, `coupons`, `coupon_redemptions` |
| Alerting | `alert_rules`, `alert_recipients`, `alert_events` |

Email is the only unique identity on a user (ignoring case). The username is
just a display name, so two people may share one.

## Keeping it current

`schema.sql` was **generated**, not hand-written: it is the exact result of the
project's own setup (the Python models, the Go server's `plan_transactions`
table, then migrations 0001–0006), dumped with `pg_dump`. That is why it
can't drift from what the code expects — and why it needs regenerating when
the schema changes:

1. Add your change as a new numbered file in `migrations/` (that's how
   existing databases will get it).
2. Load the current `schema.sql` into a scratch database, apply the new
   migration to it, and dump it:
   ```bash
   createdb scratch
   psql -d scratch -f database/schema.sql
   psql -d scratch -f migrations/0007_your_change.sql
   pg_dump -d scratch --schema-only --no-owner --no-privileges > /tmp/new.sql
   ```
3. Replace the body of `schema.sql` (everything between the "already in use"
   guard and the "Starting data" section) with `/tmp/new.sql`, removing any
   `\restrict` / `\unrestrict` lines and the
   `SELECT pg_catalog.set_config('search_path', ...)` line.

## Already includes migration 0007

`schema.sql` now contains everything through `migrations/0007_extra_alert_rules.sql`
(5 extra alert rules: stuck job, slow extraction, DB pool, DB p99, and an
*inactive* throughput-drop rule). A fresh install needs no migration files at
all. `migrations/` is only for upgrading a database that already exists.

## Includes login tracking (`login_events`)

`schema.sql` also creates `login_events` (one row per successful login, written
by the Go server) so you can see daily / weekly / monthly active users.
`usage_queries.sql` in this folder has ready-made queries, including
"registered users who actually uploaded a document":

```bash
psql -d checkmybond -f database/usage_queries.sql
```
