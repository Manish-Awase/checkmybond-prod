# REFUNDS_AND_EXPIRY_RUNBOOK.md

Two procedures for the owner: (1) proving refunds actually get recorded,
and (2) what to do to the token ledger when you deduct credits by hand
before issuing a refund.

Read this alongside `COUPON_AND_ALERTING.md` §6 (token expiry) — this
runbook assumes you know what a "grant" is (`token_grants`, one row per
credit event, some with an `expires_at`, most without).

---

## 0. How this actually works (read this first)

- A refund is never initiated from inside this app. You issue it in the
  **Stripe Dashboard** or the **Razorpay Dashboard**, same as any other
  merchant action there.
- The gateway then calls a **webhook** on your server
  (`/api/payment/webhook/stripe` or `/api/payment/webhook/razorpay`).
  That webhook is what records the refund in `plan_transactions`
  (`status` → `refunded` or `partially_refunded`, `refunded_amount` set
  to what the gateway reports).
- **Recording a refund never touches `token_balance` or `token_grants`.**
  That's deliberate — the gateway has no idea how many of the credits
  from that purchase are still unused, already spent, or should be
  clawed back at all. That decision is yours, and it's Part 2 below.
- The owner dashboard (`/accounts`) shows every refund under its
  **Refunds** section, pulling from the same table the webhook writes
  to.

---

## Part 1 — Verify refunds are actually working

Do this once after deploying, and again any time you change
`STRIPE_WEBHOOK_SECRET`, `RAZORPAY_WEBHOOK_SECRET`, or the webhook URLs
registered with either gateway.

### 1.1 Stripe

1. In the Stripe Dashboard, switch to **Test mode**.
2. Go to **Developers → Webhooks**, open your endpoint
   (`https://yourdomain.com/api/payment/webhook/stripe`), and confirm
   `charge.refunded` is in its list of listened-for events. If it isn't,
   add it — the handler only fires for events the endpoint is actually
   subscribed to.
3. Make a real test purchase through the app (any plan; the $5 minimum
   "per-field" purchase is the cheapest). Note the transaction id shown
   after payment, or find it via `SELECT id FROM plan_transactions ORDER
   BY created_at DESC LIMIT 1;`.
4. In the Dashboard, find that charge (**Payments**) and click
   **Refund** — do a **partial** refund first (e.g. refund $1 of $5).
5. Open **Developers → Webhooks → [your endpoint] → recent deliveries**.
   You should see a `charge.refunded` delivery with a **200** response
   within a few seconds. If it shows a non-200, see Troubleshooting
   below before continuing.
6. Confirm it landed:
   ```sql
   SELECT id, status, total_amount, refunded_amount, refunded_at
   FROM plan_transactions WHERE id = '<the transaction id>';
   ```
   Expect `status = 'partially_refunded'`, `refunded_amount = 1.00`.
7. Now refund the **remaining** $4 from the Dashboard. Re-run the same
   query: expect `status = 'refunded'`, `refunded_amount = 5.00`.
8. Open `/accounts` in the app (owner login) → **Refunds** section. The
   same transaction should appear with the same amount and status. This
   is the same data as step 6, just confirming the owner-facing view
   isn't stale or broken.

### 1.2 Razorpay

1. Switch to **Test mode** in the Razorpay Dashboard.
2. **Settings → Webhooks**, open your endpoint
   (`https://yourdomain.com/api/payment/webhook/razorpay`), confirm
   `refund.created` and `refund.processed` are both checked.
3. Make a real test purchase (Razorpay test cards:
   https://razorpay.com/docs/payments/payments/test-card-upi-details/).
4. In the Dashboard, find the payment (**Payments**) and issue a
   **partial** refund.
5. **Settings → Webhooks → [endpoint] → recent deliveries** — confirm a
   `refund.created` delivery shows success. Razorpay retries a failing
   endpoint on a backoff schedule for up to ~24 hours, so a delivery
   that initially failed can still arrive correctly later if you fix the
   underlying issue in that window.
6. Same SQL check as Stripe's step 6.
7. Refund the rest, confirm `status = 'refunded'` the same way.
8. Check `/accounts` → Refunds, same as Stripe's step 8.

### 1.3 What "working" means, precisely

- A **partial** refund shows `status = 'partially_refunded'` with
  `refunded_amount` less than `total_amount`.
- A **full** refund (whether issued as one refund or several partial
  ones adding up to the total) shows `status = 'refunded'`.
- Refunding the same charge **twice in the Dashboard by mistake**, or a
  gateway retrying the same webhook delivery, must **not** double the
  recorded amount — `refunded_amount` only ever reports the gateway's
  cumulative total, not an add-on-top. If you want to confirm this
  directly: trigger the same refund event twice (Stripe CLI's
  `stripe trigger charge.refunded` run twice is the easiest way) and
  confirm `refunded_amount` is identical after both.

### Troubleshooting a failed delivery

- **401/signature failure in the delivery log** → `STRIPE_WEBHOOK_SECRET`
  / `RAZORPAY_WEBHOOK_SECRET` doesn't match what's configured on that
  specific endpoint in the gateway's dashboard. Each endpoint has its
  own secret — copying the wrong one (e.g. from a different endpoint, or
  from the API keys page instead of the webhook page) is the most common
  cause.
- **200 response but nothing changed in the database** → check your
  server logs for a line starting `stripe webhook:` or `razorpay
  webhook:`. The handler logs precisely why (`matches no transaction in
  this app`, a parse error, etc.) rather than failing silently.
- **"matches no transaction in this app"** → the refund is on a payment
  this server never created a `plan_transactions` row for (e.g. a manual
  charge created directly in the Dashboard, unrelated to this app). This
  is expected and not a bug — there's nothing to record it against.

---

## Part 2 — Deducting credits by hand, then refunding

Your workflow: decide how many credits to claw back, remove them
yourself, *then* go issue the refund in the gateway dashboard. This
section is the exact, safe way to do the first part, plus what to do
about that grant's expiry date afterward.

### 2.1 Find the grant tied to the purchase

Every purchase creates one row in `token_grants` with the same id as the
`plan_transactions` row that caused it:

```sql
SELECT id, user_id, plan_id, fields_granted, fields_remaining,
       granted_at, expires_at
FROM token_grants
WHERE transaction_id = '<the transaction id you are refunding>';
```

- If `fields_remaining` is already `0`, the user has used all of it —
  there's nothing left to claw back from this grant specifically (they
  may still have a positive `token_balance` from other purchases; that's
  unrelated to this refund).
- If the plan was `free`, `per-field`, or `per-3-field` (no expiry),
  `expires_at` will be `NULL` — that's normal, skip step 2.3 for those.

### 2.2 Deduct the credits (one transaction, both tables together)

Decide `N` = how many fields you're clawing back (usually
`fields_remaining` from above, if you're reclaiming everything left; a
smaller number if you're only reclaiming part of it).

```sql
BEGIN;

UPDATE token_grants
SET fields_remaining = fields_remaining - N
WHERE id = '<grant id from step 2.1>'
  AND fields_remaining >= N;   -- guard: refuses to go negative

UPDATE users
SET token_balance = token_balance - N
WHERE id = '<user_id from step 2.1>'
  AND token_balance >= N;      -- same guard, on the cached total

COMMIT;
```

Check both `UPDATE`s reported `1` row affected before committing — if
either reports `0`, something doesn't match what you expected (stale
`fields_remaining`/`token_balance`, or the guard caught a mistake), and
you should `ROLLBACK` and re-check step 2.1 instead of committing a
partial change.

### 2.3 Decide what to do with the expiry date

The grant's `expires_at` is untouched by step 2.2 — reducing how many
fields are left doesn't change when they'd have lapsed. Two cases:

**Usually: leave it alone.** If you deducted everything
(`fields_remaining` now `0`), the expiry date is moot — there's nothing
left to expire, and the sweep job (`idb.StartTokenGrantSweep`) will
quietly skip a zero-remaining grant when its date passes. No action
needed.

**If you deducted only part of it**, and you specifically want the
*remaining* credits to expire sooner than originally granted (e.g. a
policy decision that a partially-refunded plan shouldn't keep its full
original term), change `expires_at` directly:

```sql
UPDATE token_grants
SET expires_at = '<new date, e.g. now() for "expire immediately">'
WHERE id = '<grant id>';
```

This is the **only** place `expires_at` should ever be hand-edited.
Don't touch `users.token_balance`'s relationship to other grants when
doing this — this statement only affects the one row.

### 2.4 Then issue the refund

Go do it in the Stripe or Razorpay dashboard, as normal. The webhook
will record it per Part 1 — nothing further to do on the database side.

### 2.5 Confirm both sides landed correctly

```sql
SELECT
  pt.id, pt.status, pt.total_amount, pt.refunded_amount,
  tg.fields_remaining, tg.expires_at,
  u.token_balance
FROM plan_transactions pt
JOIN token_grants tg ON tg.transaction_id = pt.id
JOIN users u ON u.id = pt.user_id
WHERE pt.id = '<the transaction id>';
```

Expect: `pt.status` reflects the gateway refund (`refunded` or
`partially_refunded`), `tg.fields_remaining` reflects what you deducted
in 2.2, and `u.token_balance` reflects the same deduction. These three
numbers are allowed to be "unrelated" in the sense that nothing enforces
a rule like "refund $X ⇒ deduct $X worth of fields" — that judgment call
is yours; this query is just confirming the two separate actions (your
manual deduction, and the gateway's refund) both actually took effect.
