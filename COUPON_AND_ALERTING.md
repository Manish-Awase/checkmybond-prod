# COUPON_AND_ALERTING.md

What's in this delivery, and what to do with it.

## 1. Coupon service

Two kinds of coupon:

- **Discount** (`percent` or `flat`) — applied at checkout. The user
  enters a code on the Plans/checkout page (`PaymentCalculationPage`);
  `/api/payment/calculate` shows the discounted price, and
  `/api/payment/pay` reserves the coupon, charges the discounted amount,
  and releases the reservation automatically if the charge fails or a
  Razorpay verification never completes.
- **Tokens** — grants free fields directly, no purchase involved.
  Redeemed on the standalone Coupons page (`/coupons`).

Admin generation (single codes or bulk batches up to 5,000) lives at
`/admin/coupons` — `CouponAdminPage.tsx`. Batches insert in chunks of
500 rows so a full 5,000-code batch is ~10 database round trips rather
than 5,000.

Redemption is atomic (`SELECT ... FOR UPDATE` inside a transaction), so
two simultaneous redemptions of a single-use code can't both succeed.

## 2. Alerting / observability service

- `internal/metrics` — HTTP, DB, Redis, and generic-span instrumentation,
  a `/metrics` endpoint in Prometheus text format, and structured JSON
  logging with trace IDs that carry from the gateway through to the
  backend.
- `internal/alerting` — a 24-metric catalog (error rates, latencies,
  queue depth, SLA breaches, confidence drift, payment/coupon health,
  goroutines, ...), a rule engine that evaluates on a timer and emails
  whoever's subscribed once a threshold holds for long enough, and an
  events table for firing/resolved history.
- Recipients (`alert_recipients` table, managed from `/admin/alerts`)
  can be scoped to one rule or left global, and filtered by minimum
  severity — so a DB-down alert can go to the DBA while a general
  error-rate warning goes to the wider team.
- `backend/monitoring` — the same instrumentation on the Python/Celery
  side, in the same `bondqi_*` metric namespace, so a Grafana query can
  span both languages without special-casing either.

Prometheus + Grafana configs are in `ops/` — see `ops/README.md` to run
them, `ops/INTEGRATION.md` for environment variables.

## 3. Before you run anything

> **Brand-new install?** Skip the migrations below. `database/schema.sql`
> builds the whole database in one command — see `database/README.md`. The
> numbered migrations are only for bringing an **existing** database up to date.

```bash
# 1. Run the two new migrations
psql "$DATABASE_URL" -f migrations/0003_coupons.sql
psql "$DATABASE_URL" -f migrations/0004_alerting.sql
psql "$DATABASE_URL" -f migrations/0005_token_grants.sql
psql -v ON_ERROR_STOP=1 "$DATABASE_URL" -f migrations/0006_username_not_unique.sql   # run BEFORE deploying the new server build

# 2. Go dependencies (see caveat below)
cd hitl-go && go mod tidy && go build ./...

# 3. Python dependency (already added to requirements.txt)
pip install -r backend/requirements.txt

# 4. Frontend — nothing new to install; the new pages use libraries
#    already in package.json (react-query, react-router)
```

Then add at least one email recipient from `/admin/alerts` — no
recipients are seeded, so alerts will fire but nobody gets mailed until
you add one.

## 4. Caveat: Go code is untested against the real toolchain here

This environment couldn't reach `proxy.golang.org`, so `go mod tidy`
was never run for real and `go build ./...` was never run against the
actual `pgx`/`chi`/etc. dependencies your `go.mod` already pins.

What WAS verified in this environment: `internal/metrics`,
`internal/coupon`, and `internal/alerting` compile and pass their unit
tests (`*_test.go`, included next to each package) against hand-written
stubs of `pgx`/`pgxpool` that mirror the real API. The stubs are not
part of this delivery — only the test files are, and they'll run
against your real `pgx` once dependencies resolve normally.

Please run, before deploying:

```bash
cd hitl-go
go mod tidy
go build ./...
go vet ./...
go test ./...
```

One pre-existing oddity noticed in `go.mod` along the way, unrelated to
this work: `github.com/go-pdf/fpdf` is listed in the indirect require
block without a version — worth a look next time you touch that file.

## 5. Where everything is

```
migrations/0003_coupons.sql
migrations/0004_alerting.sql
migrations/0005_token_grants.sql

hitl-go/internal/coupon/       generation, validation, atomic redemption
hitl-go/internal/metrics/      Prometheus registry, HTTP/DB middleware, tracing
hitl-go/internal/alerting/     rule catalog, engine, email notifier
hitl-go/internal/email/alert.go
hitl-go/routers/coupons.go     /api/coupons/*
hitl-go/routers/alerts.go      /api/alerts/*
hitl-go/routers/payment.go     coupon integration + real token expiry (monthly/annual)
hitl-go/routers/payment_webhooks.go  webhook credits now go through GrantTokens too
hitl-go/routers/profile.go     /api/profile/balance now returns expiringGrants
hitl-go/routers/account.go     coupon ADMIN routes now live here (owner-only)
hitl-go/internal/db/db.go      GrantTokens/DeductTokens/ListActiveGrants/SweepExpiredGrants
hitl-go/internal/planstore/    free-claim race fix (Postgres unique index + MySQL equivalent)
hitl-go/cmd/server/main.go     wiring + token grant expiry sweep
hitl-go/gateway/main.go        wiring

migrations/0005_token_grants.sql   dated credit ledger backing token_balance
migrations/0006_username_not_unique.sql   email is the only unique identity
database/schema.sql      the whole database in one file, for a new install
database/verify.sql      read-only checklist: did it come up right?
database/README.md       how to launch it fresh, make the first admin

backend/monitoring/metrics.py  includes bondqi_llm_cost_usd_total
backend/monitoring/instrument.py
backend/services/llm_client.py (patched — records real $ cost per call)
backend/utils/timing.py        (patched — now also records a histogram)
backend/workers/celery_app.py  (patched — starts metrics server)

tauri-app/frontend/src/pages/CouponAdminPage.tsx   owner-only (HTTP Basic Auth), NOT role-gated
tauri-app/frontend/src/pages/CostCalculatorPage.tsx  owner-only, at /accounts/cost-calculator
tauri-app/frontend/src/pages/CouponRedeemPage.tsx
tauri-app/frontend/src/pages/AlertsPage.tsx
tauri-app/frontend/src/pages/AccountsPage.tsx      (patched — fixed a pre-existing double
                                                     "Basic " auth-header bug)
tauri-app/frontend/src/pages/ProfilePage.tsx       (fixed a genuine pre-existing syntax error —
                                                     see caveat below — plus token-expiry display)
tauri-app/frontend/src/pages/UploadPage.tsx        (patched — nearest-expiry hint)
tauri-app/frontend/src/pages/PaymentCalculationPage.tsx  (patched — coupon field + expiry date at checkout)
tauri-app/frontend/src/lib/api.ts                        (patched — new API functions)
tauri-app/frontend/src/App.tsx                           (patched — routes + nav)

# Note: the repo also has a second, separate frontend/ tree. Only
# tauri-app/frontend was updated with the three new pages above — if
# frontend/ needs the same screens, CouponAdminPage.tsx, CouponRedeemPage.tsx,
# and AlertsPage.tsx will need porting over (they only depend on ThemeContext,
# theme.ts, and lib/api.ts, all of which already exist in both trees).

ops/prometheus/prometheus.yml
ops/prometheus/rules/bondqi-alerts.yml  includes BondQiLLMCostApproachingRevenueFloor
ops/grafana/dashboards/bondqi-overview.json
ops/grafana/provisioning/
ops/docker-compose.observability.yml
ops/README.md
ops/INTEGRATION.md
```

## 6. Token expiry — "monthly"/"annual" plans

Previously, `AddTokens` just added to a single running balance forever —
"Monthly" and "Annual" were misleadingly named: nothing about them
actually expired. That's now real:

- **Free, per-field, and bundle** credits still never expire.
- **Monthly** credits expire 30 days after purchase; **Annual**, 365 days.
- A new `token_grants` table (migration `0005_token_grants.sql`) records
  every credit event with its own expiry date. `token_balance` stays the
  fast cached total everything already reads — nothing that reads it
  needed to change.
- `DeductTokens` spends soonest-expiring credits first, so a mixed
  balance (some expiring, some not) doesn't waste the ones on a clock.
- An hourly sweep (`idb.StartTokenGrantSweep`, started in `main.go`)
  actually reclaims expired grants — without it running, nothing removes
  a lapsed grant's fields from the balance on its own.
- **The user sees the exact date**, in three places:
  - Checkout (`PaymentCalculationPage.tsx`) — before buying, "these N
    fields expire on DATE."
  - Profile page — after buying, the balance card lists every
    still-active expiring grant, soonest first.
  - Upload page — a compact "N expire MMM D" hint next to the balance,
    at the moment it's most relevant.
  
  This is never stale: `/api/profile/balance`'s `expiringGrants` list is
  computed fresh from the database on every request (only grants with
  `fields_remaining > 0` and not yet expired) — there's no cache to
  invalidate after a new purchase, and an expired grant simply stops
  appearing in the list rather than needing to be explicitly cleared
  from anywhere.

**A file-sync note, unrelated to this feature but found while building
it:** the very first `bondqi-prod-package.zip` you uploaded had a
genuine syntax error in `ProfilePage.tsx` (`"react-router-dom"` and the
next `import` ran together on one line with no separator — confirmed
with `tsc`, not just skimmed) that would have broken the whole frontend
build. Your later `pages.rar` upload already had this fixed, so this
delivery is now synced to that corrected version, with the token-expiry
UI layered on top. Worth checking whether that same bug exists anywhere
this delivery didn't touch, if `bondqi-prod-package.zip`'s copy is still
in use elsewhere.

## 7. LLM cost vs. plan pricing — can a "better" model turn a purchase into a loss?

Short answer: **no, not by a meaningful margin** — at current plan pricing,
LLM inference cost is a small fraction of what a field extraction is
charged. The real risk to margin isn't model tier, it's retries,
oversized documents, and free-tier abuse (see below).

**Current plan pricing** (`hitl-go/routers/payment.go`):

| Plan | Revenue per field |
|---|---|
| Pay per field | $0.50 |
| Bundle (3 fields) | $0.333 |
| Monthly (200 fields/mo) | $0.25 |
| Annual (2400 fields/yr) | $0.208 |
| Free grant | $0 (30 fields, one-time) |

**Extraction is one LLM call per field**, not per document
(`backend/services/extractor.py`'s `extract_field`, `top_k=3` retrieved
chunks, `max_tokens=256`). That's the unit that matters here.

**Cost per field, at the model prices your own `llm_client.py` documents**,
assuming a typical ~1,200–2,000 input tokens (3 retrieved chunks + prompt
template) and up to 256 output tokens (worst case — real field values are
usually far shorter):

| Model | Cost per field (typical → worst case) |
|---|---|
| groq/gpt-oss-120b | ~$0.0003 → $0.001 |
| gemini-3.1-pro | ~$0.003 → $0.007 |
| gpt-5.6-terra | ~$0.003 → $0.008 |
| claude-sonnet-5 | ~$0.005 → $0.008 |

Even at the **priciest model and the worst-case token count**, cost per
field tops out around **$0.008** — against a **$0.208–$0.50** revenue
floor, that's roughly a **96–98% margin**. Switching from a cheap model to
"medium or better" moves this by a few tenths of a cent, not by enough to
threaten profitability under this pricing table.

**What actually threatens margin** (none of these are about model
choice):

- **Retries.** `services/reextractor.py`'s retry/escalation strategy
  means a field that fails validation gets re-extracted — possibly on a
  *more* expensive model on the second attempt ("different_model"
  strategy). A field that retries 3× before succeeding costs 3× — still
  cheap in absolute terms, but worth watching if failure rates climb.
- **Oversized documents.** The estimate above assumes normal-sized
  chunks. A field extracted from an unusually dense or poorly-chunked
  document could pull far more than 3×500 tokens of context.
- **The free tier.** 30 fields free, $0 revenue, is a guaranteed
  per-signup cost (worst case ~$0.24 at Claude Sonnet 5 pricing) — normal
  customer-acquisition cost, but only sound if free-signup abuse is kept
  in check (see the free-claim race-condition fix in this delivery).
- **100%-discount coupons stacked with cheap plans** remove the margin
  entirely for whatever they're used on — another reason coupon
  generation is now owner-only.

**What's now in place to monitor this for real, not just estimate it
once:** `services/llm_client.py`'s `complete()` now records the *actual*
dollar cost of every LLM call — using each provider's own reported token
usage, not an estimate — as `bondqi_llm_cost_usd_total` (labeled by
model). `ops/prometheus/rules/bondqi-alerts.yml` has a new
`BondQiLLMCostApproachingRevenueFloor` rule that fires if average cost
per field crosses 50% of the cheapest plan's per-field revenue, and the
Grafana dashboard has two new panels for it. If a price change, a model
swap, or a retry-storm ever actually threatens the margin, you'll see it
and get alerted well before it's a real loss, rather than needing to
recompute the estimate above by hand.

Update `PRICING_USD_PER_1M` in `llm_client.py` when provider pricing
changes, and the `0.208`/`0.104` constants in the Prometheus rule and
dashboard if the plan table changes.

## 8. Rename to CheckMyBond, favicon, and the owner refunds view

**Rename** — every purely-cosmetic "BondQi" occurrence (25 across 16
files: page titles, nav label, email subject lines, alert notification
subjects, the Tauri window title and `productName`) is now
"CheckMyBond". Two things were deliberately **left unchanged** because
they're not cosmetic — renaming them without more care would break real
infrastructure:

- **`tauri.conf.json`'s `identifier` (`com.bondqi.app`)** — this is the
  desktop app's OS-level bundle identity. Changing it makes Windows/macOS
  treat new builds as a completely different application from what's
  currently installed — existing users would not auto-update, they'd
  need to manually reinstall, and any OS-level per-app state tied to the
  old identifier could be orphaned. Change this deliberately, not as a
  side effect of a branding pass, and expect to handle migration for
  existing installs if you do.
- **`Manish-Awase/bondqi-releases`** (in `DownloadPage.tsx` and
  `tauri.conf.json`'s updater endpoint) — a real GitHub repo that
  `release.yml` actually publishes installers to, and that the desktop
  app's auto-updater actually polls. Renaming the string without
  renaming (or recreating) the real repo breaks the download page and
  the updater for every existing install. Both files have a comment
  marking exactly where to make this change together, if you rename the
  repo.

**Favicon** — there wasn't one; the tab would have shown a blank icon.
Added a simple checkmark SVG (`tauri-app/frontend/public/favicon.svg`),
in the app's own accent color — not a designed logo, just enough that
the tab isn't blank. Swap the file for real branding later; nothing else
needs to change to pick it up.

**Owner dashboard additions** (`/accounts` — the same page as the users
list):
- **Refunds** — a new section listing every transaction with
  `status = 'refunded'` (set by Stripe/Razorpay's refund webhooks),
  grouped by currency rather than summed into one number (Stripe and
  Razorpay transactions can be in different currencies — adding $50 to
  ₹4000 would have been a real, silently-wrong bug).
- **Blocked users** — turned out to need nothing new: the existing
  Accounts page already has an Active/Inactive/All filter over the same
  user list `/api/account/users` already returns. Click "Inactive."

**Platform-tracking (website vs. desktop app)** — scoped, not built:
there's no column anywhere today recording which client a user is on.
Building it would mean adding a `platform` field to a login/session
event, populated by each frontend (web vs. Tauri) at login time, plus a
migration and a query surface. Real, contained work — say the word if
you want it built.

## 9. Refund tracking, and the owner's manual-deduction workflow

Previously, a refund only ever set `plan_transactions.status = 'refunded'`
— no amount, no partial-refund handling, and (a real bug, found and
fixed this pass) the webhook lookup only worked if the gateway happened
to echo back our own transaction id; a refund issued straight from the
Stripe/Razorpay **dashboard**, with no app-side trigger, could silently
fail to match anything.

What's different now:

- **Partial refunds are tracked properly.** `plan_transactions` gained
  `refunded_amount` (cumulative, in that transaction's own currency) and
  `refunded_at`. Status becomes `partially_refunded` or `refunded`
  depending on the amount, not just "was there a refund event at all."
  Recording is idempotent and monotonic (a replayed or out-of-order
  webhook can't double-count or move the amount backwards) — verified
  against a real Postgres and a real MariaDB, not just asserted.
- **Dashboard-issued refunds are matched reliably.** Lookup tries your
  own transaction id first, then falls back to the gateway's own
  payment id (`GetByGatewayTransactionID`) — so a refund started in
  Stripe's or Razorpay's dashboard, with nothing app-side involved,
  still gets recorded.
- **A refunded transaction can't be "un-refunded" by a late webhook or a
  replayed client request.** Two guards: gateway webhooks ignore any
  purchase-flow event (`payment_intent.succeeded`, `payment.captured`)
  on a row already marked refunded, and `VerifyHandler` now rejects
  `/api/payment/verify` on an already-refunded transaction — otherwise a
  refunded customer could replay their own still-valid Razorpay payment
  signature and get the credits granted a second time while keeping the
  refund.
- **Recording a refund never touches `token_balance` or `token_grants`.**
  Deliberately — see `REFUNDS_AND_EXPIRY_RUNBOOK.md` for why, and the
  exact steps for deducting credits by hand before you refund, including
  what (if anything) to do to that grant's expiry date afterward.
- **The owner dashboard's Refunds section** now shows the real refunded
  amount and status per transaction, not just "refunded" with the
  original charge amount.

New columns arrive automatically on next startup (`EnsureSchema`'s
`ALTER TABLE ... ADD COLUMN IF NOT EXISTS`, same mechanism as the
free-claim fix) — no manual migration step, and pre-existing `refunded`
rows are backfilled once as full refunds so they don't show as "refunded
for $0."

**Testing note:** the payload-parsing and refund-arithmetic logic
(`routers/refund_payloads.go`) is covered by 11 unit tests, including
Razorpay's own published sample payload for `refund.created` (its
"notes" field is an empty JSON **array**, `[]`, not an object — a plain
`map[string]string` fails to unmarshal that, which would have made the
whole webhook body fail to parse). The full store implementation
(`RecordRefund`, `GetByGatewayTransactionID`, `ListRefunds`, and the
schema backfill) was verified against a real Postgres 16 and a real
MariaDB — 36 checks total — not just the pure-function unit tests. See
`REFUNDS_AND_EXPIRY_RUNBOOK.md` for how to verify it yourself against
your own Stripe/Razorpay test-mode accounts.

## 10. Bug recheck on the refund work (§9)

Asked to recheck, so I did — read every line of the webhook handlers
fresh rather than trusting the earlier pass, and found two real issues:

1. **Owner dashboard showed the wrong amount for a partial refund.**
   `routers/account.go`'s `/refunds` endpoint was already sending the
   correct `refunded_amount` and `status` fields, but the frontend
   (`AccountsPage.tsx`) was still reading the old `total_amount` field —
   the original purchase price, not what was actually refunded. A partial
   $20 refund on a $50 purchase would have displayed as "$50." Fixed:
   the dashboard now shows the real refunded amount, with "full refund"
   or "partial · of $50.00" alongside it, and the refund date separately
   from the purchase date.
2. **Dead code**: `isCreditedStatus` was written, documented, and unit
   tested, but never actually called — both webhook handlers still used
   a bare `existing.Status == "succeeded"` inline check instead. Not a
   functional bug on its own (the refund-status guard earlier in each
   handler already filters out the cases where the difference would
   matter), but it meant the "already credited" definition lived in two
   places that could silently drift apart. Now both handlers route
   through the one documented helper.

Re-verified after both fixes: full `go build`/`go vet`/`go test ./...`
across the whole module against the real dependencies, plus a fresh
rerun of the real-Postgres integration checks from §9 (18 checks, all
passing) and a `tsc` syntax check on the frontend fix.

**Also noted, not fixed (pre-existing, outside this session's scope):**
in both webhook handlers, if `GetTransaction` returns "not found" for a
transaction id that looks like one of ours but genuinely isn't in the
database, the handler responds 500 — which makes Stripe/Razorpay retry
forever on something retrying can never fix. Worth a look if you see
either gateway's dashboard reporting a webhook endpoint with a high
failure rate on a transaction id that turns out not to exist.


## 11. Email is the only unique identity; names are just names

**The rule now:** an account is identified by its **email address**,
and nothing else is unique. Two different people can register with the
same name. The username is a display name — shown in the app, never used
to find an account.

There is **no phone number** anywhere in the schema or forms, so nothing
about phone was added. If you later want it as a second identity, it
needs its own nullable column with a partial unique index
(`UNIQUE ... WHERE phone IS NOT NULL`) so accounts without one don't
collide with each other.

### What changed

| Where | Before | Now |
|---|---|---|
| Database | unique on `username` **and** `email` | unique on `email` only, case-insensitive (`ux_users_email_lower`) |
| Register | rejected a taken username; no email format check | no name check; email must be a real address; a duplicate email is a clean 409 |
| Login | tried username first, then email | **email only** |
| Login form | "Username or email" | "Email" |
| Register forms | "Username" | "Display name" (with a note that it needn't be unique) |

**Login had to become email-only.** With names allowed to repeat, a
username can match many accounts and the database would silently hand
back whichever row it found first — you could sign in to the wrong
account, or never be able to sign in to your own. That's why there is
deliberately no `GetUserByUsername` any more. As a side effect this
closes an existing hole: someone could register the username
`victim@gmail.com`, and because login checked username first, the
victim's own email login resolved to the attacker's row.

### Run this before deploying

```bash
psql -v ON_ERROR_STOP=1 "$DATABASE_URL" -f migrations/0006_username_not_unique.sql
```

- Finds and drops any single-column unique rule on `username` **by what
  it covers, not by name** — so it works whether your database has the
  `uq_users_username` constraint, a unique index, a partial index, or a
  `lower(username)` index. A composite rule that merely *includes*
  username is left alone and reported as a WARNING.
- **Refuses to run** (changing nothing — it's one transaction) if two
  accounts already share an email ignoring case, and tells you the query
  to find them. Merging accounts isn't something a migration should
  decide.
- Safe to re-run.
- **Order matters.** If the new server build runs *before* the migration,
  the old username constraint is still there, so a second person with a
  taken name gets a generic "could not create user" (the server log names
  `uq_users_username`). Nothing is corrupted; run the migration and retry.

A fresh install needs nothing: `create_all()` builds the same constraints
from the updated model (`backend/models/db.py`).

### Things worth knowing

- **Existing users who signed in with their username must now use their
  email.** There's no username fallback by design (it would silently
  break the moment someone else picks the same name). Tell your users.
- **Older accounts aren't locked out.** Some rows were stored before
  email normalization existed (mixed case, or Gmail with dots). They used
  to get in by username. Lookup is now case-insensitive and also tries
  the address exactly as typed, so they still work with no data rewrite.
- **The name `owner` (your `OWNER_USERNAME`) can't be registered.** The
  dashboard refuses to deactivate any account with that display name, and
  you sign in as owner with Basic Auth, not through a user row — so that
  guard only ever protected whoever grabbed the name. With names no longer
  unique, anyone could have claimed it and become impossible to block.
  If an account with that name **already exists**, rename or deactivate it
  with SQL.
- Google sign-in already keyed accounts by email, so it needed no change.
- Registration returns the same "email already registered" message for an
  exact duplicate, a different-case duplicate, and a Gmail dot/`+` variant.

### How this was checked

Against a real PostgreSQL 16, Redis, and the real handlers — not mocks:

- **Migration**, 32 checks over six table shapes (model-style constraint,
  unique index, partial index, expression index, composite rule, already
  migrated) plus the refuse-and-roll-back path.
- **Model vs migration:** a database built fresh from the SQLAlchemy model
  ends up with the same constraints as a migrated one.
- **End-to-end** (`routers/auth_identity_integration_test.go`): two
  accounts with the same name each sign in to their *own* account; a name
  cannot sign in; duplicate emails are rejected (exact, case, Gmail alias);
  10 simultaneous signups with one email give exactly one 201 and nine
  409s; invalid emails and `owner` are rejected; legacy-style stored
  emails still sign in. It passes on a fresh database, **fails** on a
  database still in the old shape (so it genuinely detects the old
  behaviour), and passes again after the migration.

Run it yourself (opt-in; touches only rows it creates, so it's safe on a
database with other data):

```bash
IDENTITY_TEST_DATABASE_URL=postgres://... REDIS_HOST=localhost \
JWT_PRIVATE_KEY_PATH=... JWT_PUBLIC_KEY_PATH=... \
go test -tags integration ./routers/ -run TestIdentity -v
```


## 12. One-file database for a fresh install — and a serious sign-up hole found on the way

### The database file

`database/schema.sql` builds all 25 tables, the indexes, and the 12 starting
alert rules in one command, for a brand-new database:

```bash
createdb checkmybond
psql -v ON_ERROR_STOP=1 -d checkmybond -f database/schema.sql
psql -d checkmybond -f database/verify.sql      # optional checklist
```

It replaces the "run migrations 0001–0006 in order, but only after the
server has started once" routine. That routine has traps: `0003` adds
columns to `plan_transactions`, a table the Go server only creates when it
starts, and `0002`'s `password_resets` table isn't safe to run twice.

It is **generated, not hand-written**: I built a database the project's own
way (Python models → Go server's `plan_transactions` → migrations 0001–0006)
and dumped it, so it can't drift from what the code expects. See
`database/README.md` for launching, making the first admin, and regenerating
it when the schema changes.

Checked on a real PostgreSQL 16:
- loads into an empty database with one command, no errors, 25 tables;
- **byte-identical** to the database built the project's own way (1,315 lines
  compared) and the 12 seed rules match migration `0004`;
- refuses to run against a database that's already in use, changing nothing;
- an injected failure at the very end leaves **zero** tables (one transaction);
- the app's own startup steps (Python `init_db`, Go `EnsureSchema`) both find
  nothing to do on it and leave the schema untouched;
- on it, the real code paths work: sign-up and login, two accounts with one
  name, token grants/spend/expiry sweep, a partial refund found by the
  gateway's payment id, a single-use coupon, and the alert rules.

### The sign-up hole (not caused by this work — fixed because you're about to launch)

While writing "how to make the first admin" I found that **anyone could make
themselves an admin.** `/api/auth/register` is public (the gateway lets it
through without a login) and the server took the account's `role` straight
from the request. One request with `"role": "admin"` created an admin account.
The "Register Employee" page's role picker was only guarded in the frontend,
which anyone bypasses by calling the API directly. I reproduced it against the
old code, then fixed it:

- **Public sign-up now always creates an analyst**, whatever `role` is sent.
- **New admin-only endpoint `POST /api/auth/users`** creates an account with a
  chosen role (analyst, reviewer, admin, debug; anything else is rejected). The
  gateway verifies the caller's login for it and the server requires the admin
  role.
- **The RegisterEmployee page now uses that endpoint.** It also no longer
  swaps the admin's browser into the account they just created, which the old
  page did by reusing the public sign-up call.
- Creating a privileged account is written to the server log, with who did it.

The tests fail on the old handler (`public signup asking for role "admin"
produced role "admin"`) and pass on the new one: a stranger asking for
admin/debug/reviewer gets analyst; the create-account endpoint is 401 without a
login and 403 for any non-admin; an admin can create each role; unknown roles
and duplicate emails are rejected.

**If you already have a live database, check whether anyone used this.** List
every account that isn't an ordinary analyst and make sure you recognise each
one:

```sql
SELECT id, email, username, role, created_at
FROM users WHERE role <> 'analyst' ORDER BY created_at;
```

Anything you don't recognise: set `is_active = false` and look into it.
