// frontend/src/pages/ResetPasswordPage.tsx
import { useState, FormEvent, useEffect, useContext } from "react"
import { Link, useSearchParams, useNavigate } from "react-router-dom"
import { ThemeContext } from "../App"
import { theme, FONT_DISPLAY, FONT_BODY, FONT_IMPORT } from "../theme"

export default function ResetPasswordPage() {
  const { dark } = useContext(ThemeContext)
  const c = theme(dark)
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const token = searchParams.get("token") || ""

  const [password, setPassword] = useState("")
  const [confirm, setConfirm] = useState("")
  const [showPw, setShowPw] = useState(false)
  const [error, setError] = useState("")
  const [success, setSuccess] = useState(false)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!token) setError("Invalid or missing reset token. Please request a new reset link.")
  }, [token])

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (!token) { setError("Invalid or missing reset token."); return }
    if (!password.trim()) { setError("Please enter a new password."); return }
    if (password.length < 8) { setError("Password must be at least 8 characters."); return }
    if (password !== confirm) { setError("Passwords do not match."); return }

    setLoading(true)
    setError("")
    try {
      const res = await fetch("/api/auth/reset-password", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token, new_password: password }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error(data.detail || "Reset failed")
      setSuccess(true)
      setTimeout(() => navigate("/login"), 2500)
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
          {success ? (
            <div>
              <h2 style={{ fontFamily: FONT_DISPLAY, fontSize: "20px", fontWeight: 700, color: c.ink, margin: "0 0 8px" }}>
                Password updated
              </h2>
              <p style={{ fontSize: "13px", color: c.muted, lineHeight: 1.6, margin: "0 0 12px" }}>
                Your password has been reset. Redirecting you to sign in…
              </p>
              <Link to="/login" style={{ color: c.verified, textDecoration: "none", fontWeight: 600, fontSize: "13px" }}>
                Sign in now
              </Link>
            </div>
          ) : (
            <>
              <h1 style={{ fontFamily: FONT_DISPLAY, fontSize: "20px", fontWeight: 700, color: c.ink, margin: "0 0 4px" }}>
                Reset password
              </h1>
              <p style={{ fontSize: "13px", color: c.muted, margin: "0 0 22px" }}>
                Enter your new password below.
              </p>

              <form onSubmit={handleSubmit} style={{ display: "flex", flexDirection: "column", gap: "14px" }}>
                <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
                  <label style={{ fontSize: "12px", color: c.muted }}>New password</label>
                  <div style={{ position: "relative" }}>
                    <input
                      type={showPw ? "text" : "password"} autoComplete="new-password"
                      value={password}
                      onChange={e => { setPassword(e.target.value); setError("") }}
                      placeholder="••••••••" disabled={loading || !token}
                      style={{ ...inputStyle, paddingRight: "44px" }}
                    />
                    <button type="button" tabIndex={-1} onClick={() => setShowPw(v => !v)}
                      style={{ position: "absolute", right: 0, top: 0, bottom: 0, width: "44px",
                        background: "none", border: "none", cursor: "pointer", opacity: 0.6, fontSize: "12px", color: c.muted }}>
                      {showPw ? "hide" : "show"}
                    </button>
                  </div>
                </div>

                <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
                  <label style={{ fontSize: "12px", color: c.muted }}>Confirm password</label>
                  <input
                    type={showPw ? "text" : "password"} autoComplete="new-password"
                    value={confirm}
                    onChange={e => { setConfirm(e.target.value); setError("") }}
                    placeholder="••••••••" disabled={loading || !token}
                    style={inputStyle}
                  />
                </div>

                {error && (
                  <div style={{ background: "rgba(156,66,33,.08)", border: `1px solid ${c.flag}55`,
                    borderRadius: "4px", padding: "10px 14px", color: c.flag, fontSize: "13px" }}>
                    {error}
                  </div>
                )}

                <button type="submit" disabled={loading || !token} style={{
                  background: c.ink, border: "none", borderRadius: "4px", color: c.paper,
                  fontFamily: FONT_BODY, fontSize: "14px", fontWeight: 600,
                  padding: "12px", cursor: loading ? "default" : "pointer",
                  opacity: loading || !token ? 0.6 : 1,
                }}>
                  {loading ? "Updating…" : "Reset password"}
                </button>

                <p style={{ textAlign: "center", fontSize: "13px", margin: 0 }}>
                  <Link to="/login" style={{ color: c.verified, textDecoration: "none", fontWeight: 600 }}>Back to sign in</Link>
                </p>
              </form>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
