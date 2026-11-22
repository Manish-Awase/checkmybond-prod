// frontend/src/pages/UsageGuidePage.tsx
import { useContext } from "react"
import { Link } from "react-router-dom"
import { ThemeContext } from "../App"

interface GuideSection {
  title: string
  body: React.ReactNode
}

export default function UsageGuidePage() {
  const { dark } = useContext(ThemeContext)
  const c = {
    text: dark ? "#e2e8f0" : "#1e293b",
    muted: dark ? "#94a3b8" : "#64748b",
    card: dark ? "#1e293b" : "#ffffff",
    border: dark ? "#334155" : "#e2e8f0",
    bg: dark ? "#0f172a" : "#f8fafc",
    accent: "#3b82f6",
  }

  const sections: GuideSection[] = [
    {
      title: "1. Pick your platform",
      body: (
        <>
          <p>
            The <Link to="/downloads" style={{ color: c.accent }}>desktop app</Link> parses your
            document locally — the file itself never reaches our servers, only the extracted
            text and tables do. Use it when you'd rather not upload the raw document.
          </p>
          <p>
            The website version uploads the document directly and does everything server-side.
            Simpler to get started with, no install required.
          </p>
        </>
      ),
    },
    {
      title: "2. Build a schema",
      body: (
        <>
          <p>
            A schema is an Excel sheet listing the fields you want extracted, with these columns:
          </p>
          <div style={{ overflowX: "auto", margin: "12px 0" }}>
            <table style={{ borderCollapse: "collapse", fontSize: "12px", width: "100%" }}>
              <thead>
                <tr style={{ borderBottom: `2px solid ${c.border}` }}>
                  {["name", "field_type", "aliases", "required", "description"].map(h => (
                    <th key={h} style={{ textAlign: "left", padding: "6px 10px", color: c.muted }}>{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                <tr style={{ borderBottom: `1px solid ${c.border}` }}>
                  <td style={{ padding: "6px 10px" }}>Coupon Rate</td>
                  <td style={{ padding: "6px 10px" }}>percentage</td>
                  <td style={{ padding: "6px 10px" }}>Interest Rate, Coupon</td>
                  <td style={{ padding: "6px 10px" }}>TRUE</td>
                  <td style={{ padding: "6px 10px" }}>Annual interest rate</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p>
            <code>field_type</code> must be one of: <code>text</code>, <code>number</code>,{" "}
            <code>currency</code>, <code>date</code>, <code>percentage</code>, <code>ein</code>,{" "}
            <code>boolean</code>. <code>aliases</code> is comma-separated and optional —
            alternate names the field might appear under in the document. <code>required</code>{" "}
            defaults to TRUE if left blank.
          </p>
        </>
      ),
    },
    {
      title: "3. Upload and extract",
      body: (
        <p>
          Upload your document with the schema. Each field comes back with a confidence score
          and the evidence (retrieved text) it was extracted from. Fields the system isn't
          confident about are automatically queued for human review rather than guessed at —
          you'll see these under Queue.
        </p>
      ),
    },
    {
      title: "4. Bond reconciliation (optional)",
      body: (
        <>
          <p>
            If you're working with bonds specifically, upload a prospectus, term sheet, and
            trade confirmation together. The same fields are extracted from all three and
            automatically cross-checked — status per field comes back as <strong>matched</strong>,{" "}
            <strong>mismatch</strong>, or <strong>missing</strong>, with normalized values so
            "5.25%" and "0.0525" are correctly recognized as the same value.
          </p>
          <p>
            The first time you see a new issuer's document layout, you'll be asked to annotate a
            few fields — highlight the value in the document and assign it to a field. The
            system learns reusable extraction rules from that, so future filings from the same
            issuer match automatically without annotation.
          </p>
        </>
      ),
    },
    {
      title: "5. Review and correct",
      body: (
        <p>
          Anything queued for review shows the extracted value, the evidence it came from, and
          lets you accept or correct it. Corrections help — they're what the confidence system
          and template rules improve from over time.
        </p>
      ),
    },
    {
      title: "6. Running low on tokens?",
      body: (
        <p>
          1 token = 1 field extracted from 1 document. Your balance is shown in{" "}
          <Link to="/profile" style={{ color: c.accent }}>Profile</Link>, and if you run out
          mid-upload, top up from the <Link to="/select-plan" style={{ color: c.accent }}>plan picker</Link>{" "}
          — your progress isn't lost, resuming picks up exactly where it stopped.
        </p>
      ),
    },
  ]

  return (
    <div style={{ background: c.bg, minHeight: "100vh", color: c.text, padding: "3rem 2rem" }}>
      <div style={{ maxWidth: "720px", margin: "0 auto" }}>
        <Link to="/home" style={{ fontSize: "13px", color: c.muted, textDecoration: "none" }}>← Back home</Link>
        <h1 style={{ margin: "1rem 0 0.5rem" }}>Usage Guide</h1>
        <p style={{ color: c.muted, fontSize: "14px", marginBottom: "2.5rem" }}>
          Everything you need to go from a blank account to a reconciled document.
        </p>

        <div style={{ display: "flex", flexDirection: "column", gap: "1.75rem" }}>
          {sections.map(s => (
            <div key={s.title} style={{ background: c.card, border: `1px solid ${c.border}`,
              borderRadius: "12px", padding: "20px 24px" }}>
              <h2 style={{ fontSize: "16px", margin: "0 0 10px" }}>{s.title}</h2>
              <div style={{ fontSize: "13px", color: c.muted, lineHeight: 1.7 }}>
                {s.body}
              </div>
            </div>
          ))}
        </div>

        <p style={{ textAlign: "center", marginTop: "2.5rem", fontSize: "13px", color: c.muted }}>
          Still stuck? <Link to="/contact" style={{ color: c.accent }}>Contact us</Link>.
        </p>
      </div>
    </div>
  )
}
