// frontend/src/pages/SelectPlanPage.tsx
import { useState, useContext } from "react";
import { useNavigate } from "react-router-dom";
import { claimFreePlan } from "../lib/api";
import { ThemeContext } from "../App";
import { theme, FONT_DISPLAY, FONT_BODY, FONT_IMPORT } from "../theme";

// "per-3-field" (the old standalone Bundle card) is kept purchasable
// server-side (routers/payment.go's `plans` map still has it -- nothing
// there was removed) but is no longer shown as its own card here. Its
// $0.333/field rate is beaten by the Committed tier's Monthly rate
// ($0.25/field) at a similar commitment size, so showing both was asking
// the customer to do mental math to find the better deal.
export type PlanId = "free" | "per-field" | "monthly" | "annual";
export type AnyPlanId = PlanId | "per-3-field"; // "per-3-field" stays purchasable via direct link, just not shown as a card
export type BillingInterval = "monthly" | "annual";

// Flat lookup PaymentCalculationPage.tsx needs for ANY valid plan id,
// including "per-3-field" which no longer has a card on this page but
// is still a real, purchasable plan in routers/payment.go's `plans`
// map. Keep in sync with that map if you change pricing/steps there --
// the actual price math is always recalculated server-side via
// /api/payment/calculate, this is only for display text and the
// quantity-step default before that response comes back.
export interface PlanMeta {
  id: AnyPlanId;
  label: string;
  unitLabel: string;
  step: number;
}
export const ALL_PLANS: PlanMeta[] = [
  { id: "free",        label: "Free",          unitLabel: "field",              step: 1 },
  { id: "per-field",   label: "Pay per field", unitLabel: "field",              step: 10 },
  { id: "per-3-field", label: "Bundle plan",   unitLabel: "bundle of 3 fields", step: 10 },
  { id: "monthly",     label: "Monthly",       unitLabel: "monthly plan",       step: 1 },
  { id: "annual",      label: "Annual",        unitLabel: "annual plan",        step: 1 },
];

export interface SimplePlan {
  id: "free" | "per-field";
  label: string;
  price: string;
  desc: string;
  pricePerUnit: number;
  unitLabel: string;
  step: number;
  cta: string;
}

export const SIMPLE_PLANS: SimplePlan[] = [
  {
    id: "free",
    label: "Free",
    price: "$0",
    desc: "30 fields, once, no card required. Enough to run a full real document through the whole pipeline before you decide.",
    pricePerUnit: 0,
    unitLabel: "field",
    step: 1,
    cta: "Continue for free",
  },
  {
    id: "per-field",
    label: "Pay per field",
    price: "$0.5",
    desc: "Pay only for the fields you extract. 10-field minimum purchase.",
    pricePerUnit: 0.5,
    unitLabel: "field",
    step: 10,
    cta: "Buy",
  },
];

// Mirrors routers/payment.go's "monthly"/"annual" entries exactly --
// keep these two in sync with that file if pricing changes there.
interface CommittedTier {
  planId: "monthly" | "annual";
  price: string;
  fields: number;
  perFieldRate: string;
}
const COMMITTED_TIERS: Record<BillingInterval, CommittedTier> = {
  monthly: { planId: "monthly", price: "$50",  fields: 200,  perFieldRate: "$0.25/field" },
  annual:  { planId: "annual",  price: "$500", fields: 2400, perFieldRate: "$0.208/field" },
};
// Computed directly from the numbers above: 2400 fields at the Monthly
// rate ($50/200 = $0.25/field) would cost $600 if bought as repeated
// Monthly purchases; Annual charges $500 for the same 2400 fields.
const MONTHLY_RATE_PER_FIELD = 50 / 200;
const EQUIVALENT_MONTHLY_COST = COMMITTED_TIERS.annual.fields * MONTHLY_RATE_PER_FIELD; // 600
const ANNUAL_PRICE_NUM = parseFloat(COMMITTED_TIERS.annual.price.replace("$", ""));      // 500
const ANNUAL_SAVINGS_PCT = Math.round((1 - ANNUAL_PRICE_NUM / EQUIVALENT_MONTHLY_COST) * 100); // 17

export default function SelectPlanPage() {
  const navigate = useNavigate();
  const { dark } = useContext(ThemeContext);
  const c = theme(dark);
  const [claiming, setClaiming] = useState(false);
  const [claimError, setClaimError] = useState("");
  const [interval, setInterval_] = useState<BillingInterval>("monthly");

  const handleSelectSimple = async (plan: SimplePlan) => {
    if (plan.id === "free") {
      setClaiming(true);
      setClaimError("");
      try {
        await claimFreePlan();
        navigate("/upload");
      } catch (err) {
        setClaimError(err instanceof Error ? err.message : "Could not claim free tokens");
        setClaiming(false);
      }
      return;
    }
    navigate(`/payment/${plan.id}`);
  };

  const handleSelectCommitted = () => {
    navigate(`/payment/${COMMITTED_TIERS[interval].planId}`);
  };

  const quietCard: React.CSSProperties = {
    background: c.surface, border: `1px solid ${c.line}`, borderRadius: "6px",
    padding: "28px", display: "flex", flexDirection: "column", gap: "16px",
  };

  return (
    <div style={{
      minHeight: "100vh", display: "flex", flexDirection: "column",
      background: c.paper, fontFamily: FONT_BODY, color: c.ink,
      padding: "48px 24px 88px",
    }}>
      <style>{FONT_IMPORT}</style>
      <div style={{ width: "100%", maxWidth: "920px", margin: "0 auto", display: "flex", flexDirection: "column", gap: "40px", flex: 1 }}>

        <div>
          <span style={{ fontFamily: FONT_DISPLAY, fontWeight: 700, fontSize: "16px" }}>BondQi</span>
          <h1 style={{ fontFamily: FONT_DISPLAY, fontSize: "28px", fontWeight: 700, margin: "1.25rem 0 6px" }}>
            Choose a plan
          </h1>
          <p style={{ fontSize: "13px", color: c.muted, margin: 0 }}>
            Pick the pricing that fits how much you extract. You can change this later.
          </p>
        </div>

        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(260px, 1fr))", gap: "16px" }}>

          {SIMPLE_PLANS.map(plan => (
            <div key={plan.id} style={quietCard}>
              <div>
                <div style={{ fontSize: "13px", color: c.muted, marginBottom: "8px" }}>{plan.label}</div>
                <div style={{ display: "flex", alignItems: "baseline", gap: "6px" }}>
                  <span style={{ fontFamily: FONT_DISPLAY, fontSize: "30px", fontWeight: 700 }}>{plan.price}</span>
                  {plan.id !== "free" && (
                    <span style={{ fontSize: "12px", color: c.muted }}>/ {plan.unitLabel}</span>
                  )}
                </div>
              </div>
              <p style={{ fontSize: "13px", color: c.muted, margin: 0, lineHeight: 1.6, flex: 1 }}>
                {plan.desc}
              </p>
              <button
                onClick={() => handleSelectSimple(plan)}
                disabled={plan.id === "free" && claiming}
                style={{
                  background: plan.id === "free" ? "transparent" : c.ink,
                  border: plan.id === "free" ? `1px solid ${c.line}` : "none",
                  borderRadius: "4px",
                  color: plan.id === "free" ? c.ink : c.paper,
                  fontFamily: FONT_BODY, fontSize: "13px", fontWeight: 600,
                  padding: "11px", cursor: plan.id === "free" && claiming ? "default" : "pointer",
                  opacity: plan.id === "free" && claiming ? 0.6 : 1,
                }}
              >
                {plan.id === "free" && claiming ? "Claiming…" : plan.cta}
              </button>
              {plan.id === "free" && claimError && (
                <p style={{ fontSize: "12px", color: c.flag, margin: 0 }}>{claimError}</p>
              )}
            </div>
          ))}

          {/* ── Committed tier — inverted fill, the one real hierarchy signal on the page ── */}
          <div style={{
            background: c.ink, borderRadius: "6px", padding: "28px",
            display: "flex", flexDirection: "column", gap: "16px", color: c.paper,
          }}>
            <div>
              <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "8px" }}>
                <span style={{ fontSize: "13px", opacity: 0.7 }}>Committed</span>
                <div style={{ display: "flex", background: "rgba(255,255,255,.08)", borderRadius: "4px", padding: "2px" }}>
                  {(["monthly", "annual"] as BillingInterval[]).map(iv => (
                    <button
                      key={iv}
                      onClick={() => setInterval_(iv)}
                      style={{
                        border: "none", borderRadius: "3px", padding: "4px 10px",
                        fontSize: "11px", fontWeight: 600, fontFamily: FONT_BODY, cursor: "pointer",
                        background: interval === iv ? c.paper : "transparent",
                        color: interval === iv ? c.ink : c.paper,
                      }}
                    >
                      {iv === "monthly" ? "Monthly" : `Annual (save ${ANNUAL_SAVINGS_PCT}%)`}
                    </button>
                  ))}
                </div>
              </div>
              <div style={{ display: "flex", alignItems: "baseline", gap: "6px" }}>
                <span style={{ fontFamily: FONT_DISPLAY, fontSize: "30px", fontWeight: 700 }}>
                  {COMMITTED_TIERS[interval].price}
                </span>
                <span style={{ fontSize: "12px", opacity: 0.7 }}>
                  / {COMMITTED_TIERS[interval].fields.toLocaleString()} fields
                </span>
              </div>
              <div style={{ fontSize: "12px", opacity: 0.7, marginTop: "2px" }}>
                {COMMITTED_TIERS[interval].perFieldRate}
              </div>
            </div>

            <p style={{ fontSize: "13px", opacity: 0.85, margin: 0, lineHeight: 1.6, flex: 1 }}>
              A bigger one-time bundle sized for {interval === "monthly" ? "a month" : "a year"} of typical
              use — our best per-field rate outside Free. One-time purchase; doesn't auto-renew.
            </p>

            <button
              onClick={handleSelectCommitted}
              style={{
                background: c.paper, border: "none", borderRadius: "4px", color: c.ink,
                fontFamily: FONT_BODY, fontSize: "13px", fontWeight: 600,
                padding: "11px", cursor: "pointer",
              }}
            >
              Buy {interval === "monthly" ? "Monthly" : "Annual"}
            </button>
          </div>
        </div>
      </div>

      <button
        onClick={() => navigate("/upload")}
        style={{
          position: "fixed", right: "24px", bottom: "24px",
          background: c.surface, border: `1px solid ${c.line}`,
          borderRadius: "4px", color: c.muted,
          fontFamily: FONT_BODY, fontSize: "13px", fontWeight: 600,
          padding: "9px 18px", cursor: "pointer",
        }}
      >
        Skip for now
      </button>
    </div>
  );
}
