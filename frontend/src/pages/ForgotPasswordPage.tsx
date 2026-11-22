// frontend/src/pages/ForgotPasswordPage.tsx
import { useState, FormEvent, useContext } from "react"
import { Link } from "react-router-dom"
import { ThemeContext } from "../App"
import { theme, FONT_DISPLAY, FONT_BODY, FONT_IMPORT } from "../theme"

export default function ForgotPasswordPage() {
  const { dark } = useContext(ThemeContext)
  const c = theme(dark)
  const [email, setEmail] = useState("")
  const [error, setError] = useState("")
  const [sent, setSent] = useState(false)
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (!email.trim()) {
      setError("Please enter your email address.")
      return
    }
    const isEmail = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)
    if (!isEmail) {
      setError("Please enter a valid email address.")
      return
    }

    setLoading(true)
    setError("")
    try {
      const res = await fetch("/api/auth/forgot-password", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email: email.trim() }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error(data.detail || "Request failed")
      setSent(true)
    } catch (err: any) {
      setError(err.message || "Something went wrong. Please try again.")
    } finally {
      setLoading(false)
    }
  }

  const inputStyle = {
    width: "100%", background: c.paper,
    border: `1px solid ${error ? c.flag : c.line}`,
    borderRadius: "4px", color: c.ink,
    fontFamily: "monospace", fontSize: "14px",
    padding: "10px 14px", outline: "none",
    boxSizing: "border-box" as const,
  }

  return (
    <div style={{
      minHeight: "100vh", display: "flex", alignItems: "center", justifyContent: "center",
      background: c.paper, fontFamily: FONT_BODY, padding: "32px 20px",
    }}>
      <style>{FONT_IMPORT}</style>
      <div style={{ width: "100%", maxWidth: "400px", display: "flex", flexDirection: "column", gap: "20px" }}>
        <span style={{ fontFamily: FONT_DISPLAY, fontWeight: 700, fontSize: "18px", color: c.ink }}>BondQi</span>

        <div style={{ background: c.surface, border: `1px solid ${c.line}`, borderRadius: "6px", padding: "28px" }}>
          {sent ? (
            <div>
              <h2 style={{ fontFamily: FONT_DISPLAY, fontSize: "20px", fontWeight: 700, color: c.ink, margin: "0 0 8px" }}>
                Check your inbox
              </h2>
              <p style={{ fontSize: "13px", color: c.muted, lineHeight: 1.6, margin: "0 0 18px" }}>
                If an account exists for <strong style={{ color: c.ink }}>{email}</strong>, we've sent a password
                reset link. It expires in 15 minutes.
              </p>
              <Link to="/login" style={{ color: c.verified, textDecoration: "none", fontWeight: 600, fontSize: "13px" }}>
                Back to sign in
              </Link>
            </div>
          ) : (
            <>
              <h1 style={{ fontFamily: FONT_DISPLAY, fontSize: "20px", fontWeight: 700, color: c.ink, margin: "0 0 4px" }}>
                Forgot password?
              </h1>
              <p style={{ fontSize: "13px", color: c.muted, margin: "0 0 22px" }}>
                Enter your email and we'll send you a reset link.
              </p>

              <form onSubmit={handleSubmit} style={{ display: "flex", flexDirection: "column", gap: "14px" }}>
                <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
                  <label style={{ fontSize: "12px", color: c.muted }}>Email address</label>
                  <input
                    type="email" autoComplete="email" autoFocus
                    value={email}
                    onChange={e => { setEmail(e.target.value); setError("") }}
                    placeholder="you@example.com"
                    disabled={loading}
                    style={inputStyle}
                  />
                </div>

                {error && (
                  <div style={{ background: "rgba(156,66,33,.08)", border: `1px solid ${c.flag}55`,
                    borderRadius: "4px", padding: "10px 14px", color: c.flag, fontSize: "13px" }}>
                    {error}
                  </div>
                )}

                <button type="submit" disabled={loading} style={{
                  background: c.ink, border: "none", borderRadius: "4px", color: c.paper,
                  fontFamily: FONT_BODY, fontSize: "14px", fontWeight: 600,
                  padding: "12px", cursor: loading ? "default" : "pointer", opacity: loading ? 0.6 : 1,
                }}>
                  {loading ? "Sending…" : "Send reset link"}
                </button>

                <p style={{ textAlign: "center", fontSize: "13px", color: c.muted, margin: 0 }}>
                  Remember your password?{" "}
                  <Link to="/login" style={{ color: c.verified, textDecoration: "none", fontWeight: 600 }}>Sign in</Link>
                </p>
              </form>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
