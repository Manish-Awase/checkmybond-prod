// frontend/src/pages/PaymentCalculationPage.tsx
import { useState, useEffect, useRef, useContext } from "react";
import { useNavigate, useParams, Link } from "react-router-dom";
import { ALL_PLANS, AnyPlanId } from "./SelectPlanPage";
import { useAuth } from "../context/AuthContext";
import { apiFetch } from "../lib/api";
import { ThemeContext } from "../App";
import { theme, FONT_DISPLAY, FONT_BODY, FONT_IMPORT } from "../theme";
import StripeCardForm from "../components/StripeCardForm";

interface CalculationResult {
  planId: string;
  planLabel: string;
  quantity: number;
  unitLabel: string;
  pricePerUnit: number;
  totalFields: number;
  total: number;
}

interface PayResponse {
  transactionId: string;
  status: "succeeded" | "pending" | "failed";
  paymentGateway: string;
  total: number;
  currency: string;
  // Only present when status is "pending" — a client-side step is needed
  // to actually finish the payment (see interfaces.ChargeResult in Go).
  clientSecret?: string;
  gatewayOrderId?: string;
  gatewayKeyId?: string;
}

// Loads Razorpay's Checkout script on demand (only when the wired gateway
// is actually razorpay) rather than on every page load.
function loadRazorpayScript(): Promise<boolean> {
  return new Promise(resolve => {
    if ((window as any).Razorpay) return resolve(true);
    const script = document.createElement("script");
    script.src = "https://checkout.razorpay.com/v1/checkout.js";
    script.onload = () => resolve(true);
    script.onerror = () => resolve(false);
    document.body.appendChild(script);
  });
}

export default function PaymentCalculationPage() {
  const navigate = useNavigate();
  const { dark } = useContext(ThemeContext);
  const c = theme(dark);
  const { user } = useAuth(); // assumes AuthContext exposes { id, username, email }
  const { planId } = useParams<{ planId: AnyPlanId }>();
  const plan = ALL_PLANS.find(p => p.id === planId) ?? ALL_PLANS[1];

  const [quantity, setQuantity]   = useState<number>(plan.step);
  const [result,   setResult]     = useState<CalculationResult | null>(null);
  const [error,    setError]      = useState("");
  const [payError, setPayError]   = useState("");
  const [loading,  setLoading]    = useState(false);
  const [paying,   setPaying]     = useState(false);
  const [stripePayment, setStripePayment] = useState<{ clientSecret: string; publishableKey: string } | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout>>();

  // Ask the backend to price the current quantity. All plan math
  // (validation, per-unit pricing, field totals) lives server-side.
  const fetchCalculation = async (qty: number) => {
    setLoading(true);
    setError("");
    try {
      const data = await apiFetch<CalculationResult>("/api/payment/calculate", {
        method: "POST",
        body: JSON.stringify({ planId: plan.id, quantity: qty }),
      });
      setResult(data);
    } catch (err) {
      setResult(null);
      setError(err instanceof Error ? err.message : "Could not calculate price");
    } finally {
      setLoading(false);
    }
  };

  // Debounce calculation requests as the user types.
  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => fetchCalculation(quantity), 350);
    return () => { if (debounceRef.current) clearTimeout(debounceRef.current); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [quantity, plan.id]);

  const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setQuantity(Number(e.target.value));
  };

  const bump = (dir: 1 | -1) => {
    setQuantity(q => Math.max(plan.step, q + dir * plan.step));
  };

  const handlePay = async () => {
    if (!result || !user) return;
    setPaying(true);
    setPayError("");
    setStripePayment(null);
    try {
      const data = await apiFetch<PayResponse>("/api/payment/pay", {
        method: "POST",
        body: JSON.stringify({
          // userId is intentionally NOT sent — the backend derives it from
          // the verified X-User-Id header (see imw.Identity), never from
          // client-supplied JSON.
          username: user.username,
          email: user.email,
          planId: plan.id,
          quantity,
          currency: "USD",
        }),
      });

      if (data.status === "succeeded") {
        // Already written to plan_transactions as "succeeded" on the backend.
        navigate("/upload");
        return;
      }

      if (data.status === "pending" && data.paymentGateway === "razorpay") {
        await payWithRazorpay(data);
        return;
      }

      if (data.status === "pending" && data.paymentGateway === "stripe") {
        if (!data.clientSecret || !data.gatewayKeyId) {
          setPayError("Stripe did not return the details needed to collect payment. Please try again.");
          return;
        }
        // Render the card form below instead of navigating — the charge
        // isn't done until the customer enters card details and Stripe
        // confirms the PaymentIntent client-side.
        setStripePayment({ clientSecret: data.clientSecret, publishableKey: data.gatewayKeyId });
        return;
      }

      setPayError("Payment could not be completed.");
    } catch (err) {
      setPayError(err instanceof Error ? err.message : "Could not reach the payment server");
    } finally {
      setPaying(false);
    }
  };

  // Opens Razorpay's Checkout widget for a "pending" order, then verifies
  // the result with the backend before navigating away.
  const payWithRazorpay = async (data: PayResponse) => {
    const ok = await loadRazorpayScript();
    if (!ok || !data.gatewayOrderId || !data.gatewayKeyId) {
      setPayError("Could not load the payment widget. Please try again.");
      return;
    }

    const Razorpay = (window as any).Razorpay;
    const rzp = new Razorpay({
      key: data.gatewayKeyId,
      order_id: data.gatewayOrderId,
      amount: Math.round(data.total * 100),
      currency: data.currency,
      name: "BondQi",
      description: `${plan.label} — ${quantity} ${plan.unitLabel}${quantity === 1 ? "" : "s"}`,
      prefill: { name: user?.username, email: user?.email },
      // Notes on the Checkout call land on the resulting *payment* entity
      // (order-level notes don't automatically carry over) — the
      // Razorpay webhook handler reads this back to find our transaction.
      notes: { transactionId: data.transactionId },
      handler: async (response: {
        razorpay_order_id: string;
        razorpay_payment_id: string;
        razorpay_signature: string;
      }) => {
        try {
          await apiFetch(`/api/payment/verify`, {
            method: "POST",
            body: JSON.stringify({
              transactionId: data.transactionId,
              fields: {
                razorpay_order_id: response.razorpay_order_id,
                razorpay_payment_id: response.razorpay_payment_id,
                razorpay_signature: response.razorpay_signature,
              },
            }),
          });
          navigate("/upload");
        } catch (err) {
          setPayError(
            (err instanceof Error ? err.message : "Payment verification failed") +
            " — contact support with transaction id " + data.transactionId
          );
        }
      },
      modal: {
        ondismiss: () => setPayError("Payment was cancelled"),
      },
    });
    rzp.open();
  };

  const inputStyle = {
    width: "100%", padding: "10px 14px",
    background: c.paper, color: c.ink,
    border: `1px solid ${error ? c.flag : c.line}`,
    borderRadius: "4px", fontSize: "14px",
    fontFamily: "monospace",
    outline: "none", boxSizing: "border-box" as const,
    transition: "border-color .15s, box-shadow .15s",
    textAlign: "center" as const,
  };

  return (
    <div style={{
      minHeight: "100vh", display: "flex", alignItems: "center", justifyContent: "center",
      background: c.paper, fontFamily: FONT_BODY,
      color: c.ink, padding: "32px 20px",
    }}>
      <style>{FONT_IMPORT}</style>
      <div style={{ width: "100%", maxWidth: "440px", display: "flex", flexDirection: "column", gap: "20px" }}>

        <span style={{ fontFamily: FONT_DISPLAY, fontWeight: 700, fontSize: "16px" }}>BondQi</span>

        {/* ── Card ── */}
        <div style={{
          background: c.surface, border: `1px solid ${c.line}`,
          borderRadius: "6px", padding: "28px",
        }}>
          <div style={{ fontSize: "13px", color: c.muted, marginBottom: "6px" }}>
            {plan.label}
          </div>
          <h1 style={{ fontFamily: FONT_DISPLAY, fontSize: "22px", fontWeight: 700, margin: "0 0 4px" }}>
            Calculate your price
          </h1>
          <p style={{ fontSize: "13px", color: c.muted, margin: "0 0 24px" }}>
            {plan.step === 1
              ? "Adjust the quantity below if you want more than one, or continue with the default."
              : `Enter your quantity in multiples of ${plan.step}.`}
          </p>

          <div style={{ display: "flex", flexDirection: "column", gap: "16px" }}>

            {/* Quantity */}
            <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
              <label style={{ fontSize: "12px", color: c.muted }}>
                Quantity ({plan.unitLabel}{quantity === 1 ? "" : "s"}) — multiples of {plan.step}
              </label>
              <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
                <button
                  type="button" onClick={() => bump(-1)}
                  style={{
                    width: "38px", height: "38px", flexShrink: 0,
                    background: c.paper, border: `1px solid ${c.line}`,
                    borderRadius: "4px", fontSize: "16px", fontWeight: 700,
                    color: c.ink, cursor: "pointer",
                  }}
                >−</button>
                <input
                  type="number" step={plan.step} min={plan.step}
                  value={quantity} onChange={handleChange}
                  style={inputStyle}
                />
                <button
                  type="button" onClick={() => bump(1)}
                  style={{
                    width: "38px", height: "38px", flexShrink: 0,
                    background: c.paper, border: `1px solid ${c.line}`,
                    borderRadius: "4px", fontSize: "16px", fontWeight: 700,
                    color: c.ink, cursor: "pointer",
                  }}
                >+</button>
              </div>
              {error && <span style={{ fontSize: "11px", color: c.flag }}>{error}</span>}
            </div>

            {/* Breakdown — populated entirely from the backend response */}
            <div style={{
              background: c.paper, border: `1px solid ${c.line}`,
              borderRadius: "4px", padding: "14px 16px",
              display: "flex", flexDirection: "column", gap: "8px",
              minHeight: "90px", justifyContent: loading ? "center" : "flex-start",
            }}>
              {loading && (
                <span style={{ fontSize: "12px", color: c.muted, textAlign: "center" }}>Calculating…</span>
              )}
              {!loading && result && (
                <>
                  <Row muted={c.muted} ink={c.ink} label="Plan" value={result.planLabel} />
                  <Row muted={c.muted} ink={c.ink} label="Price per unit" value={`$${result.pricePerUnit.toFixed(2)}`} />
                  <Row muted={c.muted} ink={c.ink} label="Quantity" value={`${result.quantity} ${result.unitLabel}${result.quantity === 1 ? "" : "s"}`} />
                  {plan.id === "per-3-field" && (
                    <Row muted={c.muted} ink={c.ink} label="Fields covered" value={`${result.totalFields} fields`} />
                  )}
                  <div style={{ height: "1px", background: c.line, margin: "2px 0" }} />
                  <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline" }}>
                    <span style={{ fontSize: "13px", fontWeight: 700 }}>Total</span>
                    <span style={{ fontFamily: FONT_DISPLAY, fontSize: "22px", fontWeight: 700 }}>
                      ${result.total.toFixed(2)}
                    </span>
                  </div>
                </>
              )}
              {!loading && !result && !error && (
                <span style={{ fontSize: "12px", color: c.muted, textAlign: "center" }}>Enter a quantity to see pricing</span>
              )}
            </div>

            {/* Pay error */}
            {payError && (
              <div style={{
                background: "rgba(156,66,33,.08)", border: `1px solid ${c.flag}55`,
                borderRadius: "4px", padding: "10px 14px", fontSize: "12px", color: c.flag,
              }}>
                {payError}
              </div>
            )}

            {/* Stripe card entry — shown once /api/payment/pay has created a
                PaymentIntent and returned a clientSecret to confirm client-side */}
            {stripePayment && result && (
              <StripeCardForm
                clientSecret={stripePayment.clientSecret}
                publishableKey={stripePayment.publishableKey}
                amountLabel={`$${result.total.toFixed(2)}`}
                onSuccess={() => navigate("/upload")}
              />
            )}

            {/* Pay button — hidden once Stripe card entry has taken over */}
            {!stripePayment && (
              <button
                onClick={handlePay} disabled={paying || loading || !result || !user}
                style={{
                  background: c.ink, border: "none", borderRadius: "4px", color: c.paper,
                  fontFamily: FONT_BODY, fontSize: "14px", fontWeight: 600,
                  padding: "12px", cursor: paying || loading || !result || !user ? "default" : "pointer",
                  display: "flex", alignItems: "center", justifyContent: "center", gap: "8px",
                  opacity: paying || loading || !result || !user ? 0.5 : 1,
                }}
              >
                {paying && (
                  <span style={{
                    width: "14px", height: "14px", borderRadius: "50%",
                    border: `2px solid ${c.paper}55`, borderTopColor: c.paper,
                    animation: "spin .7s linear infinite", display: "inline-block",
                  }} />
                )}
                {paying ? "Processing…" : result ? `Pay $${result.total.toFixed(2)}` : "Pay"}
              </button>
            )}

            <p style={{ textAlign: "center", fontSize: "13px", color: c.muted, margin: 0 }}>
              <Link to="/select-plan" style={{ color: c.verified, textDecoration: "none", fontWeight: 600 }}>
                Choose a different plan
              </Link>
            </p>
          </div>
        </div>
      </div>

      <style>{`
        @keyframes spin { to { transform: rotate(360deg); } }
        input[type=number]::-webkit-inner-spin-button,
        input[type=number]::-webkit-outer-spin-button { -webkit-appearance: none; margin: 0; }
        input[type=number] { -moz-appearance: textfield; }
      `}</style>
    </div>
  );
}

function Row({ muted, ink, label, value }: { muted: string; ink: string; label: string; value: string }) {
  return (
    <div style={{ display: "flex", justifyContent: "space-between" }}>
      <span style={{ fontSize: "12px", color: muted }}>{label}</span>
      <span style={{ fontSize: "12px", color: ink, fontFamily: "monospace" }}>{value}</span>
    </div>
  );
}
