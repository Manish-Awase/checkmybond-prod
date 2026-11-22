// frontend/src/pages/LandingPage.tsx
//
// Design rationale (see also theme.ts): this product's core promise is
// precision and evidence, not hype -- it's an institutional fixed-income
// tool (bond prospectuses, term sheets, confirmations), used by analysts
// and ops teams who trust numbers over marketing copy. The design leans
// into that: a cool "ledger paper" background instead of the generic
// warm-cream SaaS default, a serif display face (evokes the actual
// documents this reads), emerald/rust instead of blue/red for
// verified/flagged states, and a hero that SHOWS the product's actual
// mechanic (a field extraction with its confidence score and evidence)
// rather than abstract icon cards. Deliberately avoids: uppercase
// eyebrow labels, arrow-suffixed buttons, identical rounded cards with
// uniform shadow, and centered marketing-page hero text.
import { useContext } from "react"
import { Link } from "react-router-dom"
import { ThemeContext } from "../App"
import { theme, FONT_DISPLAY, FONT_BODY, FONT_IMPORT } from "../theme"

export default function LandingPage() {
  const { dark } = useContext(ThemeContext)
  const c = theme(dark)

  const benefits = [
    {
      title: "Evidence, not guesses",
      body: "Every extracted value ships with the exact text it came from and a confidence score. Low-confidence fields are queued for a person to check, never silently assumed.",
    },
    {
      title: "Reconciled across documents",
      body: "Upload a prospectus, term sheet, and confirmation together — the same field is compared across all three, and disagreements are flagged automatically, with values normalized so \"5.25%\" and \"0.0525\" are correctly read as identical.",
    },
    {
      title: "Learns your issuers",
      body: "Annotate a layout once and the system reuses it — a filing from the same issuer next quarter matches instantly, without repeating the setup.",
    },
    {
      title: "Runs on your machine, if you'd rather",
      body: "The desktop app parses documents locally. The file itself never reaches our servers — only the extracted text does.",
    },
  ]

  return (
    <div style={{ background: c.paper, minHeight: "100vh", color: c.ink, fontFamily: FONT_BODY }}>
      <style>{FONT_IMPORT}</style>

      {/* ── Nav ── */}
      <nav style={{ display: "flex", justifyContent: "space-between", alignItems: "center",
        padding: "22px 40px", borderBottom: `1px solid ${c.line}` }}>
        <span style={{ fontFamily: FONT_DISPLAY, fontWeight: 700, fontSize: "19px" }}>BondQi</span>
        <div style={{ display: "flex", gap: "28px", alignItems: "center", fontSize: "14px" }}>
          <Link to="/guide" style={{ color: c.ink, textDecoration: "none" }}>Guide</Link>
          <Link to="/downloads" style={{ color: c.ink, textDecoration: "none" }}>Download</Link>
          <Link to="/contact" style={{ color: c.ink, textDecoration: "none" }}>Contact</Link>
          <Link to="/login" style={{ color: c.ink, textDecoration: "none" }}>Log in</Link>
          <Link to="/register" style={{ padding: "9px 20px", background: c.ink, color: c.paper,
            borderRadius: "4px", textDecoration: "none", fontWeight: 600, fontSize: "13px" }}>
            Get started
          </Link>
        </div>
      </nav>

      {/* ── Hero: left copy, right extraction-preview mockup ── */}
      <section style={{ maxWidth: "1120px", margin: "0 auto", padding: "5.5rem 40px 4.5rem",
        display: "grid", gridTemplateColumns: "1fr 1fr", gap: "48px", alignItems: "center" }}>
        <div>
          <h1 style={{ fontFamily: FONT_DISPLAY, fontSize: "44px", fontWeight: 700,
            lineHeight: 1.12, margin: "0 0 1.25rem", maxWidth: "480px" }}>
            Extract what's in the document, not what a model assumes.
          </h1>
          <p style={{ fontSize: "16px", color: c.muted, lineHeight: 1.65, margin: "0 0 2rem", maxWidth: "440px" }}>
            BondQi reads bond prospectuses, term sheets, and confirmations, extracts the fields
            you specify, and reconciles them across documents — with the evidence and confidence
            behind every value, so you can trust what you're looking at.
          </p>
          <div style={{ display: "flex", gap: "12px", marginBottom: "1rem" }}>
            <Link to="/register" style={{ padding: "13px 26px", background: c.ink, color: c.paper,
              borderRadius: "4px", textDecoration: "none", fontWeight: 600, fontSize: "14px" }}>
              Start free
            </Link>
            <Link to="/downloads" style={{ padding: "13px 26px", background: "transparent", color: c.ink,
              border: `1px solid ${c.line}`, borderRadius: "4px", textDecoration: "none",
              fontWeight: 600, fontSize: "14px" }}>
              Download desktop app
            </Link>
          </div>
          <p style={{ fontSize: "13px", color: c.muted }}>
            30 fields free, once. No card required.
          </p>
        </div>

        {/* Extraction preview mockup */}
        <div style={{ background: c.surface, border: `1px solid ${c.line}`, borderRadius: "6px",
          padding: "22px 24px", boxShadow: dark ? "none" : "0 1px 3px rgba(18,21,28,.06)" }}>
          <div style={{ fontSize: "11px", color: c.muted, marginBottom: "14px", fontFamily: "monospace" }}>
            prospectus.pdf — page 12
          </div>
          {[
            { field: "Coupon Rate", value: "5.25%", conf: 98, evidence: "\u201c...the Notes bear interest at the rate of 5.25% per annum, payable semi-annually...\u201d" },
            { field: "Maturity Date", value: "2034-06-15", conf: 96, evidence: "\u201c...the Notes will mature on June 15, 2034, unless earlier redeemed...\u201d" },
            { field: "Credit Rating", value: "BBB+", conf: 71, evidence: "\u201c...rated BBB+ by S&P as of the date of this prospectus...\u201d", review: true },
          ].map(row => (
            <div key={row.field} style={{ padding: "12px 0", borderTop: `1px solid ${c.line}` }}>
              <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline" }}>
                <span style={{ fontSize: "12px", color: c.muted }}>{row.field}</span>
                <span style={{ display: "flex", alignItems: "center", gap: "6px" }}>
                  <span style={{ width: "6px", height: "6px", borderRadius: "50%",
                    background: row.review ? c.flag : c.verified, display: "inline-block" }} />
                  <span style={{ fontSize: "11px", color: c.muted }}>{row.conf}%</span>
                </span>
              </div>
              <div style={{ fontSize: "16px", fontWeight: 600, margin: "3px 0 5px" }}>{row.value}</div>
              <div style={{ fontSize: "11px", color: c.muted, fontStyle: "italic", lineHeight: 1.5 }}>
                {row.evidence}
              </div>
            </div>
          ))}
        </div>
      </section>

      {/* ── Benefits: divided list, not identical cards ── */}
      <section style={{ maxWidth: "1120px", margin: "0 auto", padding: "1rem 40px 5rem" }}>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(2, 1fr)", gap: "1px",
          background: c.line, border: `1px solid ${c.line}` }}>
          {benefits.map(b => (
            <div key={b.title} style={{ background: c.paper, padding: "28px 32px" }}>
              <h3 style={{ fontFamily: FONT_DISPLAY, fontSize: "18px", fontWeight: 600, margin: "0 0 8px" }}>
                {b.title}
              </h3>
              <p style={{ margin: 0, fontSize: "13px", color: c.muted, lineHeight: 1.65 }}>
                {b.body}
              </p>
            </div>
          ))}
        </div>
      </section>

      {/* ── How it works — genuinely a sequence, so numbered ── */}
      <section style={{ maxWidth: "760px", margin: "0 auto", padding: "0 40px 6rem" }}>
        <h2 style={{ fontFamily: FONT_DISPLAY, fontSize: "24px", fontWeight: 700, marginBottom: "2rem" }}>
          How it works
        </h2>
        <div style={{ display: "flex", flexDirection: "column" }}>
          {[
            { n: "1", title: "Upload", body: "Bring a document and a field schema — an Excel sheet of what to extract, or annotate a document once to teach a new layout." },
            { n: "2", title: "Extract", body: "Fields come back with confidence and evidence. Anything uncertain is queued for a person to review instead of guessed at." },
            { n: "3", title: "Reconcile", body: "For bonds, upload a prospectus, term sheet, and confirmation together — mismatches across all three are flagged." },
            { n: "4", title: "Review and use", body: "Confirm or correct queued fields, then pull the results into your own workflow." },
          ].map((s, i, arr) => (
            <div key={s.n} style={{ display: "flex", gap: "20px", paddingBottom: i < arr.length - 1 ? "28px" : 0 }}>
              <div style={{ fontFamily: FONT_DISPLAY, fontSize: "20px", color: c.muted, width: "24px", flexShrink: 0 }}>
                {s.n}
              </div>
              <div>
                <h3 style={{ margin: "0 0 4px", fontSize: "15px", fontWeight: 600 }}>{s.title}</h3>
                <p style={{ margin: 0, fontSize: "13px", color: c.muted, lineHeight: 1.6, maxWidth: "520px" }}>
                  {s.body}
                </p>
              </div>
            </div>
          ))}
        </div>
        <p style={{ marginTop: "2rem" }}>
          <Link to="/guide" style={{ color: c.verified, fontWeight: 600, fontSize: "14px", textDecoration: "none" }}>
            Read the full usage guide
          </Link>
        </p>
      </section>

      {/* ── Footer ── */}
      <footer style={{ borderTop: `1px solid ${c.line}`, padding: "24px 40px",
        display: "flex", justifyContent: "space-between", flexWrap: "wrap", gap: "12px",
        fontSize: "13px", color: c.muted }}>
        <span>© {new Date().getFullYear()} BondQi</span>
        <div style={{ display: "flex", gap: "20px" }}>
          <Link to="/guide" style={{ color: c.muted }}>Guide</Link>
          <Link to="/contact" style={{ color: c.muted }}>Contact</Link>
          <Link to="/privacy" style={{ color: c.muted }}>Privacy Policy</Link>
          <Link to="/terms" style={{ color: c.muted }}>Terms of Service</Link>
        </div>
      </footer>
    </div>
  )
}
