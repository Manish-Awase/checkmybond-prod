// frontend/src/pages/ProfilePage.tsx
import { useState, useEffect, useContext } from "react"
import { useNavigate } from "react-router-dom"import { getTokenBalance, submitFeedback as sendFeedback, deleteAccount as requestAccountDeletion } from "../lib/api"
import { useAuth } from "../context/AuthContext"
import { ThemeContext } from "../App"

export default function ProfilePage() {
  const { dark } = useContext(ThemeContext)
  const { user, logout } = useAuth()
  const navigate = useNavigate()

  const [balance, setBalance]       = useState<number | null>(null)
  const [balanceErr, setBalanceErr] = useState("")

  // Feedback form
  const [kind, setKind]         = useState<"feedback" | "complaint">("feedback")
  const [message, setMessage]   = useState("")
  const [sending, setSending]   = useState(false)
  const [sent, setSent]         = useState(false)
  const [sendErr, setSendErr]   = useState("")

  // Delete-account dialog
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [reason, setReason]           = useState("")
  const [rating, setRating]           = useState<number | null>(null)
  const [deleting, setDeleting]       = useState(false)
  const [deleteErr, setDeleteErr]     = useState("")
  const [typedConfirm, setTypedConfirm] = useState("")

  useEffect(() => {
    getTokenBalance()
      .then(d => setBalance(d.token_balance))
      .catch(e => setBalanceErr(e instanceof Error ? e.message : "Could not load balance"))
  }, [])

  const submitFeedback = async () => {
    if (!message.trim()) return
    setSending(true)
    setSendErr("")
    try {
      await sendFeedback({ kind, message })
      setSent(true)
      setMessage("")
      setTimeout(() => setSent(false), 4000)
    } catch (e) {
      setSendErr(e instanceof Error ? e.message : "Could not submit — please try again")
    } finally {
      setSending(false)
    }
  }

  const confirmDelete = async () => {
    setDeleting(true)
    setDeleteErr("")
    try {
      await requestAccountDeletion({
        reason: reason.trim() || undefined,
        rating: rating ?? undefined,
      })
      logout()
      navigate("/login", { replace: true })
    } catch (e) {
      setDeleteErr(e instanceof Error ? e.message : "Could not delete account — please try again")
      setDeleting(false)
    }
  }

  const c = {
    text:   dark ? "#e2e8f0" : "#1e293b",
    muted:  dark ? "#94a3b8" : "#64748b",
    card:   dark ? "#1e293b" : "#ffffff",
    border: dark ? "#334155" : "#e2e8f0",
    bg:     dark ? "#0f172a" : "#f8fafc",
  }

  return (
    <div style={{ maxWidth: "560px", margin: "0 auto", padding: "2rem", color: c.text }}>
      <h1 style={{ marginBottom: "0.25rem" }}>Profile</h1>
      <p style={{ color: c.muted, fontSize: "13px", marginBottom: "2rem" }}>{user?.username} · {user?.email}</p>

      {/* ── Token balance ────────────────────────────────────────────────── */}
      <div style={{ padding: "1rem", background: c.bg, borderRadius: "8px",
        border: `1px solid ${c.border}`, marginBottom: "1.5rem" }}>
        <h3 style={{ margin: "0 0 8px", fontSize: "14px", fontWeight: 600 }}>Field-Extraction Balance</h3>
        {balanceErr ? (
          <p style={{ color: "#dc2626", fontSize: "12px" }}>{balanceErr}</p>
        ) : (
          <div style={{ fontSize: "28px", fontWeight: 700, color: "#16a34a" }}>
            {balance === null ? "…" : balance} <span style={{ fontSize: "13px", color: c.muted, fontWeight: 400 }}>tokens</span>
          </div>
        )}
        <p style={{ fontSize: "11px", color: c.muted, marginTop: "6px" }}>
          1 token = 1 field extracted from 1 document. Buy more from the plan picker if you run out mid-upload —
          your Resume button on the Upload page will pick up right where it stopped.
        </p>
        <button onClick={() => navigate("/select-plan")}
          style={{ marginTop: "10px", padding: "7px 16px", background: "#16a34a", color: "#fff",
            border: "none", borderRadius: "6px", fontSize: "12px", fontWeight: 600, cursor: "pointer" }}>
          Buy more tokens
        </button>
      </div>

      {/* ── Feedback / complaint ─────────────────────────────────────────── */}
      <div style={{ padding: "1rem", background: c.bg, borderRadius: "8px",
        border: `1px solid ${c.border}`, marginBottom: "1.5rem" }}>
        <h3 style={{ margin: "0 0 10px", fontSize: "14px", fontWeight: 600 }}>Feedback &amp; Complaints</h3>
        <div style={{ display: "flex", gap: "8px", marginBottom: "10px" }}>
          {(["feedback", "complaint"] as const).map(k => (
            <button key={k} onClick={() => setKind(k)}
              style={{ padding: "6px 14px", borderRadius: "6px", fontSize: "12px", fontWeight: 600,
                border: `1px solid ${kind === k ? "#3b82f6" : c.border}`,
                background: kind === k ? "rgba(59,130,246,.1)" : "transparent",
                color: kind === k ? "#3b82f6" : c.muted, cursor: "pointer", textTransform: "capitalize" }}>
              {k}
            </button>
          ))}
        </div>
        <textarea value={message} onChange={e => setMessage(e.target.value)}
          placeholder={kind === "complaint" ? "What went wrong?" : "What's on your mind?"}
          rows={4}
          style={{ width: "100%", boxSizing: "border-box", padding: "10px", borderRadius: "6px",
            border: `1px solid ${c.border}`, background: c.card, color: c.text, fontSize: "13px",
            fontFamily: "inherit", resize: "vertical", marginBottom: "8px" }} />
        {sendErr && <p style={{ color: "#dc2626", fontSize: "12px", marginBottom: "8px" }}>{sendErr}</p>}
        {sent && <p style={{ color: "#16a34a", fontSize: "12px", marginBottom: "8px" }}>✓ Thanks — received.</p>}
        <button onClick={submitFeedback} disabled={sending || !message.trim()}
          style={{ padding: "8px 20px", background: "#3b82f6", color: "#fff", border: "none",
            borderRadius: "6px", fontSize: "13px", fontWeight: 600,
            cursor: sending || !message.trim() ? "not-allowed" : "pointer",
            opacity: sending || !message.trim() ? 0.5 : 1 }}>
          {sending ? "Sending…" : "Submit"}
        </button>
      </div>

      {/* ── Danger zone ──────────────────────────────────────────────────── */}
      <div style={{ padding: "1rem", background: "rgba(220,38,38,.04)", borderRadius: "8px",
        border: "1px solid rgba(220,38,38,.25)" }}>
        <h3 style={{ margin: "0 0 6px", fontSize: "14px", fontWeight: 600, color: "#dc2626" }}>Delete Account</h3>
        <p style={{ fontSize: "12px", color: c.muted, marginBottom: "10px" }}>
          Deactivates your account immediately. Your document/correction history is kept for records — this
          isn't a full data wipe — but you won't be able to log in again with these credentials.
        </p>
        <button onClick={() => setConfirmOpen(true)}
          style={{ padding: "8px 18px", background: "transparent", color: "#dc2626",
            border: "1px solid #dc2626", borderRadius: "6px", fontSize: "13px", fontWeight: 600, cursor: "pointer" }}>
          Delete my account
        </button>
      </div>

      {/* ── Confirm dialog ───────────────────────────────────────────────── */}
      {confirmOpen && (
        <div style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,.55)",
          display: "flex", alignItems: "center", justifyContent: "center", zIndex: 1000 }}>
          <div style={{ width: "380px", background: c.card, borderRadius: "10px", padding: "24px",
            border: `1px solid ${c.border}` }}>
            <h3 style={{ margin: "0 0 6px", fontSize: "15px", color: "#dc2626" }}>Delete your account?</h3>
            <p style={{ fontSize: "12px", color: c.muted, marginBottom: "14px" }}>
              This can't be undone from the app — you'd need to contact support to reactivate.
            </p>

            <label style={{ fontSize: "11px", color: c.muted, display: "block", marginBottom: "4px" }}>
              Why are you leaving? (optional, helps us improve)
            </label>
            <textarea value={reason} onChange={e => setReason(e.target.value)} rows={3}
              style={{ width: "100%", boxSizing: "border-box", padding: "8px", borderRadius: "6px",
                border: `1px solid ${c.border}`, background: c.bg, color: c.text, fontSize: "12px",
                fontFamily: "inherit", resize: "vertical", marginBottom: "10px" }} />

            <div style={{ display: "flex", gap: "4px", marginBottom: "14px" }}>
              {[1, 2, 3, 4, 5].map(n => (
                <button key={n} onClick={() => setRating(n)}
                  style={{ width: "28px", height: "28px", borderRadius: "6px", cursor: "pointer",
                    border: `1px solid ${rating === n ? "#3b82f6" : c.border}`,
                    background: rating === n ? "rgba(59,130,246,.15)" : "transparent",
                    color: rating === n ? "#3b82f6" : c.muted, fontSize: "12px" }}>
                  {n}
                </button>
              ))}
            </div>

            <label style={{ fontSize: "11px", color: c.muted, display: "block", marginBottom: "4px" }}>
              Type DELETE to confirm
            </label>
            <input value={typedConfirm} onChange={e => setTypedConfirm(e.target.value)}
              style={{ width: "100%", boxSizing: "border-box", padding: "8px", borderRadius: "6px",
                border: `1px solid ${c.border}`, background: c.bg, color: c.text, fontSize: "13px",
                marginBottom: "14px" }} />

            {deleteErr && <p style={{ color: "#dc2626", fontSize: "12px", marginBottom: "10px" }}>{deleteErr}</p>}

            <div style={{ display: "flex", gap: "8px", justifyContent: "flex-end" }}>
              <button onClick={() => setConfirmOpen(false)} disabled={deleting}
                style={{ padding: "8px 16px", background: "transparent", color: c.muted,
                  border: `1px solid ${c.border}`, borderRadius: "6px", fontSize: "13px", cursor: "pointer" }}>
                Cancel
              </button>
              <button onClick={confirmDelete} disabled={deleting || typedConfirm !== "DELETE"}
                style={{ padding: "8px 16px", background: "#dc2626", color: "#fff", border: "none",
                  borderRadius: "6px", fontSize: "13px", fontWeight: 600,
                  cursor: deleting || typedConfirm !== "DELETE" ? "not-allowed" : "pointer",
                  opacity: deleting || typedConfirm !== "DELETE" ? 0.5 : 1 }}>
                {deleting ? "Deleting…" : "Delete permanently"}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}