// frontend/src/pages/TermsOfServicePage.tsx
//
// TEMPLATE CONTENT — NOT LEGAL ADVICE. Same caveat as PrivacyPolicyPage.tsx:
// grounded in what this product actually does, but not reviewed by a
// lawyer and missing jurisdiction-specific terms (governing law,
// arbitration/dispute resolution, liability caps appropriate to a
// product that processes financial documents). Do not publish as-is.
import { useContext } from "react"
import { ThemeContext } from "../App"

export default function TermsOfServicePage() {
  const { dark } = useContext(ThemeContext)
  const c = {
    text: dark ? "#e2e8f0" : "#1e293b",
    muted: dark ? "#94a3b8" : "#64748b",
    bg: dark ? "#0f172a" : "#f8fafc",
    warnBg: dark ? "rgba(245,158,11,.1)" : "rgba(245,158,11,.08)",
    warnBorder: "rgba(245,158,11,.4)",
  }

  return (
    <div style={{ background: c.bg, minHeight: "100vh", color: c.text, padding: "3rem 2rem" }}>
      <div style={{ maxWidth: "720px", margin: "0 auto", fontSize: "14px", lineHeight: 1.7 }}>
        <div style={{ padding: "14px 18px", background: c.warnBg, border: `1px solid ${c.warnBorder}`,
          borderRadius: "8px", marginBottom: "2rem", fontSize: "13px" }}>
          <strong>Draft — not yet reviewed by counsel.</strong> Have this reviewed by a
          lawyer before publishing, especially the liability and payment sections.
        </div>

        <h1>Terms of Service</h1>
        <p style={{ color: c.muted }}>Last updated: [DATE]</p>

        <h2>1. The service</h2>
        <p>
          BondQi extracts structured data from documents you or your organization
          upload, using OCR and/or LLM-based extraction, and can reconcile extracted
          values across multiple related documents. Extraction results — including
          LLM output — may contain errors and should be reviewed before being relied
          upon for financial, legal, or compliance decisions.
        </p>

        <h2>2. No warranty on extraction accuracy</h2>
        <p>
          Automated extraction, including human-reviewed fields, is provided "as is."
          [This is the single most important section for a lawyer to draft properly for
          a financial-document product — get real legal language here defining what
          liability you're taking on, if any, for extraction errors that lead to a
          user's financial decision.]
        </p>

        <h2>3. Your account</h2>
        <p>
          You're responsible for keeping your login credentials secure. We may require
          additional verification (CAPTCHA, one-time codes) after repeated failed login
          attempts as a security measure.
        </p>

        <h2>4. Payment and billing</h2>
        <p>
          Paid plans are billed via Stripe and/or Razorpay under their respective terms.
          [Add your actual refund/cancellation policy here.]
        </p>

        <h2>5. Acceptable use</h2>
        <p>
          You may not use the service to process documents you don't have the right to
          process, or to attempt to circumvent rate limits, security controls, or
          extraction quotas.
        </p>

        <h2>6. Termination</h2>
        <p>[Describe under what conditions an account may be suspended or terminated.]</p>

        <h2>7. Governing law</h2>
        <p>[This needs to be filled in with your actual jurisdiction and needs legal review — do not leave a placeholder in a live document.]</p>

        <h2>8. Contact</h2>
        <p>Questions about these terms: [SUPPORT EMAIL]</p>
      </div>
    </div>
  )
}
