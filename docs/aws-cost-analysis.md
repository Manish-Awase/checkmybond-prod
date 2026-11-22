# Project11 — AWS Cost & Revenue Analysis
**Region:** ap-south-1 (Mumbai) · **Pricing:** On-Demand Linux · **Date:** July 2026
**Exchange rate:** 1 USD = ₹95.30 (Wise, July 2026)

---

## Infrastructure Cost by User Count

| Users | Per Hour | Per Day | Per Week | Per Month |
|------:|:--------:|:-------:|:--------:|:---------:|
| **0** | $0.12 / ₹11.59 | $2.92 / ₹278 | $20.44 / ₹1.95K | $88.81 / ₹8.46K |
| **1** | $0.12 / ₹11.66 | $2.94 / ₹280 | $20.55 / ₹1.96K | $89.32 / ₹8.51K |
| **10** | $0.16 / ₹15.15 | $3.81 / ₹363 | $26.70 / ₹2.54K | $116.01 / ₹11.06K |
| **100** | $0.50 / ₹47.32 | $11.92 / ₹1.14K | $83.41 / ₹7.95K | $362.44 / ₹34.54K |
| **1,000** | $1.16 / ₹110.74 | $27.89 / ₹2.66K | $195.23 / ₹18.60K | $848.30 / ₹80.84K |
| **10,000** | $2.67 / ₹253.99 | $63.96 / ₹6.10K | $447.75 / ₹42.67K | $1,950 / ₹1.85L |
| **1,000,000** | $16.90 / ₹1.61K | $405.71 / ₹38.66K | $2,840 / ₹2.71L | $12,340 / ₹11.76L |

---

## Architecture per Tier

| Users | Gateway | API Server | Celery Worker(s) | Postgres | Redis | Extra |
|------:|:-------:|:----------:|:----------------:|:--------:|:-----:|:------|
| 0–1 | t3.micro | t3.micro | 1× t3.medium | db.t3.micro | cache.t3.micro | — |
| 10 | t3.micro | t3.small | 1× t3.medium | db.t3.micro | cache.t3.micro | ALB |
| 100 | t3.small | t3.medium | 2× m5.large | db.t3.medium | cache.t3.small | ALB + NAT |
| 1,000 | t3.medium | m5.large | 4× m5.large | db.m5.large | cache.r6g.large | 2× ALB + NAT |
| 10,000 | 2× m5.large | 3× m5.large | 4× m5.xlarge | Multi-AZ db.m5.large | 2× cache.r6g.large | 3× ALB + 2× NAT |
| 1,000,000 | 4× c5.2xlarge | 6× m5.xlarge | 20× m5.xlarge | Aurora Multi-AZ | 6× cache.r6g.large | 6× ALB + 4× NAT + S3 |

> **Why Celery needs large instances:** `sentence-transformers` embedding model
> requires ~3-4GB RAM minimum. `t3.medium` (4GB) is the absolute floor —
> any smaller and the worker OOM-kills on the first extraction.

---

## Revenue & Profitability Model

### Pricing
| Tier | What you get | Price |
|:-----|:------------|:------|
| **Free** | 4 field corrections × 2 PDFs = 8 total corrections (lifetime) | ₹0 |
| **Paid** | Unlimited corrections at ₹2 per field correction | ₹2/correction |

### Revenue per paying user per month
```
₹2 per field × 6 fields per PDF × 10 PDFs per month = ₹120/user/month
```

### Assumptions
- **20%** of registered users are active in any given month
- **30%** of active users convert to paid (after free tier exhausted)
- Average **10 PDFs/month** per active paid user, **6 fields** each
- Net effective revenue: ₹120 × 30% × 20% = **₹7.20 per registered user/month**

### Revenue vs Infrastructure Cost

| Registered Users | Active (20%) | Paying (30%) | Revenue/month | Infra Cost/month | Profit/Loss |
|----------------:|:------------:|:------------:|:-------------:|:----------------:|:-----------:|
| 0 | 0 | 0 | ₹0 | ₹8,460 | **−₹8,460** |
| 1 | 1 | 1 | ₹120 | ₹8,510 | **−₹8,392** |
| 10 | 2 | 1 | ₹120 | ₹11,060 | **−₹10,936** |
| 100 | 20 | 6 | ₹720 | ₹34,540 | **−₹33,821** |
| 1,000 | 200 | 60 | ₹7,200 | ₹80,840 | **−₹73,643** |
| 10,000 | 2,000 | 600 | ₹72,000 | ₹1,85,413 | **−₹1,13,413** |
| **1,000,000** | 2,00,000 | 60,000 | **₹72,00,000** | ₹11,76,000 | **+₹60,24,000** |

---

## Break-even Analysis

| Infra Tier | Monthly Infra Cost | Paying Users Needed | Registered Users Needed* |
|:----------:|:-----------------:|:-------------------:|:------------------------:|
| 0–1 users | ₹8,510 | 71 | 1,183 |
| 10 users | ₹11,060 | 93 | 1,550 |
| 100 users | ₹34,540 | 288 | 4,800 |
| 1,000 users | ₹80,840 | 674 | 11,233 |
| 10,000 users | ₹1,85,000 | 1,546 | 25,767 |
| 1,000,000 users | ₹11,76,000 | 9,800 | 1,63,333 |

*Registered users needed = paying users ÷ 0.30 (conversion) ÷ 0.20 (active rate)

> **Key insight:** You reach profitability only at ~1.63 lakh registered users
> on the 1M-tier infra, OR at ~11,000 registered users if you stay on the
> 1,000-user infra tier. The 1,000-user tier is the sweet spot for a
> bootstrapped startup — cheapest path to break-even.

---

## Cost Optimization Levers

### 1. Reserved Instances (biggest saving)
Commit to 1-year Reserved Instances for the always-on compute:

| Instance | On-Demand/hr | 1yr Reserved/hr | Monthly saving |
|:---------|:------------:|:---------------:|:--------------:|
| m5.large | $0.115 | $0.073 | ~37% |
| t3.medium | $0.052 | $0.032 | ~38% |
| db.m5.large | $0.253 | $0.157 | ~38% |

**Effect:** At 1,000 users, monthly cost drops from ₹80,840 → ~₹51,000

### 2. Spot Instances for Celery workers
Celery workers are **ideal** for Spot Instances — they use `acks_late=True`
so jobs are not lost if the instance is reclaimed. Spot saves 60-90% on EC2.

**Effect:** 4× m5.large Celery workers ($0.115 × 4 = $0.46/hr) →
Spot price ~$0.030/hr each = $0.12/hr total (74% saving on workers)

### 3. Fargate instead of EC2 for Go services
Go gateway + API server are stateless and lightweight — Fargate tasks
start/stop on demand, no idle EC2 cost. At 10 users with very low traffic,
Fargate may cost less than keeping t3.micro instances running 24/7.

### 4. Combined savings at 1,000 users
| Base infra | ₹80,840/month |
|:-----------|:-------------:|
| + Reserved Instances (37% off compute) | −₹18,000 |
| + Spot workers (74% off Celery EC2) | −₹12,000 |
| **Optimised total** | **~₹50,840/month** |
| Break-even paying users | **424** (vs 674 base) |
| Break-even registered users | **7,067** (vs 11,233 base) |

---

## Pricing Model Recommendation

At ₹2/field, revenue scales slowly. Alternatives to consider:

| Model | ₹/user/month (paid) | Break-even at 1K users |
|:------|:-------------------:|:----------------------:|
| ₹2/field (current) | ₹120 | 674 paying users |
| ₹5/field | ₹300 | 270 paying users |
| ₹10/field | ₹600 | 135 paying users |
| ₹99 flat/month unlimited | ₹99 | 817 paying users |
| ₹199 flat/month unlimited | ₹199 | 406 paying users |

> **Recommendation:** ₹5/field (₹300/month for avg usage) is the best
> balance — still underpriced vs enterprise OCR/AI tools (typically ₹500–2000/month)
> while making break-even achievable at ~5,000 registered users on optimised infra.

---

## Summary

```
At ₹2/field with current model:
  Profitable at:  ~1,63,000 registered users (1M-tier infra)
  OR              ~11,000 registered users (stay on 1K-tier infra)

With Spot + Reserved + ₹5/field:
  Profitable at:  ~5,000 registered users — achievable in 12–18 months
```

| Phase | Users | Monthly Cost | Monthly Revenue | Status |
|:------|:-----:|:------------:|:---------------:|:------:|
| Dev/test | 0–10 | ₹8.5K–11K | ₹0–120 | Loss (expected) |
| Early launch | 100 | ₹34.5K | ₹720 | Loss |
| Growth | 1,000 | ₹80.8K | ₹7.2K | Loss |
| Scale | 10,000 | ₹1.85L | ₹72K | Loss |
| **Profitable** | **1,00,000+** | **~₹5–7L** | **~₹7.2L+** | **Profit** |
| SaaS scale | 10,00,000 | ₹11.76L | ₹72L | **+₹60L profit** |

