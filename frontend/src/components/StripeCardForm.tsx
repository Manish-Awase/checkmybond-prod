// frontend/src/components/StripeCardForm.tsx
//
// Renders Stripe's PaymentElement against a PaymentIntent's clientSecret
// and confirms it. This is the piece that was missing before — without it,
// a "stripe" pending result from POST /api/payment/pay had no way to
// actually collect card details, so the payment could never complete.
import { useState } from "react";
import { loadStripe, Stripe } from "@stripe/stripe-js";
import {
  Elements,
  PaymentElement,
  useStripe,
  useElements,
} from "@stripe/react-stripe-js";

// Cache the Stripe.js instance per publishable key so remounting this
// component (e.g. re-rendering the page) doesn't reload the script or
// recreate the object every time.
const stripePromiseCache = new Map<string, Promise<Stripe | null>>();
function getStripe(publishableKey: string) {
  if (!stripePromiseCache.has(publishableKey)) {
    stripePromiseCache.set(publishableKey, loadStripe(publishableKey));
  }
  return stripePromiseCache.get(publishableKey)!;
}

interface StripeCardFormProps {
  clientSecret: string;
  publishableKey: string;
  amountLabel: string; // e.g. "$12.00" — just for the button text
  onSuccess: () => void;
}

export default function StripeCardForm({
  clientSecret,
  publishableKey,
  amountLabel,
  onSuccess,
}: StripeCardFormProps) {
  return (
    <Elements stripe={getStripe(publishableKey)} options={{ clientSecret }}>
      <StripeCardFormInner amountLabel={amountLabel} onSuccess={onSuccess} />
    </Elements>
  );
}

function StripeCardFormInner({
  amountLabel,
  onSuccess,
}: {
  amountLabel: string;
  onSuccess: () => void;
}) {
  const stripe = useStripe();
  const elements = useElements();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!stripe || !elements) return; // Stripe.js hasn't finished loading yet

    setSubmitting(true);
    setError("");

    // redirect: "if_required" — most card payments confirm inline; Stripe
    // only redirects for methods that need it (e.g. certain 3D Secure or
    // wallet flows), in which case return_url is where it sends them back.
    const { error: confirmError, paymentIntent } = await stripe.confirmPayment({
      elements,
      confirmParams: {
        return_url: `${window.location.origin}/upload`,
      },
      redirect: "if_required",
    });

    if (confirmError) {
      setError(confirmError.message || "Payment failed");
      setSubmitting(false);
      return;
    }

    if (paymentIntent?.status === "succeeded") {
      // The payment_intent.succeeded webhook will independently mark
      // plan_transactions as succeeded on the backend — this just moves
      // the user forward once Stripe itself confirms the charge.
      onSuccess();
      return;
    }

    // requires_action / processing / etc — Stripe.js handled what it could
    // inline; anything left needs the customer to complete something (rare
    // once redirect:"if_required" is set, but stay safe rather than assume
    // failure).
    setError(
      paymentIntent
        ? `Payment status: ${paymentIntent.status}. Please try again or use a different card.`
        : "Payment did not complete."
    );
    setSubmitting(false);
  };

  return (
    <form onSubmit={handleSubmit} style={{ display: "flex", flexDirection: "column", gap: "14px" }}>
      <PaymentElement />
      {error && (
        <div style={{
          display: "flex", alignItems: "center", gap: "8px",
          background: "rgba(239,68,68,.06)", border: "1px solid rgba(239,68,68,.3)",
          borderRadius: "8px", padding: "10px 14px", fontSize: "12px", color: "#f87171",
        }}>
          {error}
        </div>
      )}
      <button
        type="submit"
        disabled={!stripe || !elements || submitting}
        style={{
          background: "linear-gradient(135deg, #635bff, #635bffcc)", // Stripe brand purple
          border: "none", borderRadius: "10px", color: "white",
          fontFamily: "'Sora', sans-serif", fontSize: "14px", fontWeight: 700,
          padding: "11px", cursor: submitting ? "default" : "pointer",
          opacity: !stripe || !elements || submitting ? 0.6 : 1,
        }}
      >
        {submitting ? "Processing…" : `Pay ${amountLabel} with card →`}
      </button>
    </form>
  );
}
