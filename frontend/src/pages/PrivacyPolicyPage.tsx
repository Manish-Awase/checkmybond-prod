// frontend/src/pages/PrivacyPolicyPage.tsx
//
// TEMPLATE CONTENT — NOT LEGAL ADVICE. This describes what the product
// ACTUALLY does based on this codebase (local-parse desktop app vs.
// server-side website upload, Stripe/Razorpay payment data, OTP/CAPTCHA
// login security, Redis-backed session data) so it isn't generic
// boilerplate — but it has NOT been reviewed by a lawyer, doesn't cover
// jurisdiction-specific requirements (GDPR, CCPA, DPDP Act, etc.), and
// must not be published as-is. Have this reviewed before it's live,
// especially given the product handles financial documents (bonds,
// prospectuses) that may contain personal or sensitive data belonging
// to people who never interacted with BondQi directly.
import { useContext } from "react"
import { ThemeContext } from "../App"

export default function PrivacyPolicyPage() {
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
          <strong>Draft — not yet reviewed by counsel.</strong> This page is a starting
          template, not a finished legal document. Have it reviewed by a lawyer familiar
          with your jurisdiction before relying on it.
        </div>

        <h1>Privacy Policy</h1>
        <p style={{ color: c.muted }}>Last updated: [DATE]</p>

        <h2>1. What we collect</h2>
        <p>
          Account information (username, email, password hash) when you register.
          Documents and schemas you upload, if you use the website — files are stored
          temporarily to run extraction and are not retained longer than needed for
          that purpose. If you use the desktop app in LLM mode, your document is parsed
          on your own machine and only the extracted text and tables are sent to us —
          the file itself is never uploaded.
        </p>

        <h2>2. Payment information</h2>
        <p>
          Payments are processed by Stripe and/or Razorpay. We do not store your card
          number; payment processors handle that directly under their own privacy
          policies.
        </p>

        <h2>3. Login security data</h2>
        <p>
          To protect accounts, we temporarily track failed login attempts (by account
          and by IP address) and may require a CAPTCHA or a one-time email code after
          repeated failures. This data is stored briefly (typically under 15 minutes)
          and is used only for account security.
        </p>

        <h2>4. How long we keep data</h2>
        <p>
          Extraction results and account data are kept while your account is active.
          [Describe your actual retention periods and deletion process here —
          this varies by what you've actually built and by applicable law.]
        </p>

        <h2>5. Third parties</h2>
        <p>
          We use Groq for LLM-based field extraction (extracted text is sent to Groq's
          API), Stripe and/or Razorpay for payments, and may use email delivery
          providers for account and security notifications.
        </p>

        <h2>6. Your rights</h2>
        <p>
          [This section needs to reflect the specific rights applicable in your
          users' jurisdictions — e.g. GDPR (EU/UK), CCPA/CPRA (California), or India's
          DPDP Act — including how someone can request deletion or a copy of their
          data. Do not leave this as a placeholder in a live policy.]
        </p>

        <h2>7. Contact</h2>
        <p>Questions about this policy: [SUPPORT EMAIL]</p>
      </div>
    </div>
  )
}
