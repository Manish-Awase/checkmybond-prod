// frontend/src/pages/ContactPage.tsx
import { useContext, useState } from "react"
import { ThemeContext } from "../App"

export default function ContactPage() {
  const { dark } = useContext(ThemeContext)
  const [form, setForm] = useState({ name: "", email: "", message: "", website: "" }) // "website" = honeypot
  const [status, setStatus] = useState<"idle" | "sending" | "sent" | "error">("idle")
  const [errorMsg, setErrorMsg] = useState("")

  const c = {
    text: dark ? "#e2e8f0" : "#1e293b",
    muted: dark ? "#94a3b8" : "#64748b",
    card: dark ? "#1e293b" : "#ffffff",
    border: dark ? "#334155" : "#e2e8f0",
    bg: dark ? "#0f172a" : "#f8fafc",
  }

  const inputStyle = {
    width: "100%", padding: "10px 12px", borderRadius: "6px",
    border: `1px solid ${c.border}`, background: dark ? "#0f172a" : "#fff",
    color: c.text, fontSize: "13px", marginTop: "4px",
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!form.name.trim() || !form.email.trim() || !form.message.trim()) return
    setStatus("sending")
    setErrorMsg("")
    try {
      const res = await fetch("/api/contact", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(form),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        throw new Error(body.detail || "Could not send your message")
      }
      setStatus("sent")
    } catch (err) {
      setStatus("error")
      setErrorMsg(err instanceof Error ? err.message : "Something went wrong")
    }
  }

  if (status === "sent") {
    return (
      <div style={{ background: c.bg, minHeight: "100vh", color: c.text,
        display: "flex", alignItems: "center", justifyContent: "center", padding: "2rem" }}>
        <div style={{ textAlign: "center" }}>
          <h2>Message sent</h2>
          <p style={{ color: c.muted }}>We'll get back to you as soon as we can.</p>
        </div>
      </div>
    )
  }

  return (
    <div style={{ background: c.bg, minHeight: "100vh", color: c.text, padding: "3rem 2rem" }}>
      <div style={{ maxWidth: "480px", margin: "0 auto" }}>
        <h1 style={{ marginBottom: "0.25rem" }}>Contact us</h1>
        <p style={{ color: c.muted, fontSize: "14px", marginBottom: "2rem" }}>
          Questions, bug reports, or anything else — we read every message.
        </p>

        <form onSubmit={handleSubmit} style={{ background: c.card, border: `1px solid ${c.border}`,
          borderRadius: "12px", padding: "24px" }}>
          <div style={{ marginBottom: "14px" }}>
            <label style={{ fontSize: "12px", fontWeight: 600 }}>Name</label>
            <input value={form.name} onChange={e => setForm({ ...form, name: e.target.value })}
              style={inputStyle} required />
          </div>
          <div style={{ marginBottom: "14px" }}>
            <label style={{ fontSize: "12px", fontWeight: 600 }}>Email</label>
            <input type="email" value={form.email} onChange={e => setForm({ ...form, email: e.target.value })}
              style={inputStyle} required />
          </div>
          <div style={{ marginBottom: "14px" }}>
            <label style={{ fontSize: "12px", fontWeight: 600 }}>Message</label>
            <textarea rows={5} value={form.message} onChange={e => setForm({ ...form, message: e.target.value })}
              style={{ ...inputStyle, resize: "vertical" }} required />
          </div>

          {/* Honeypot — hidden from real users via CSS, bots that fill every
              field will trip this. Server rejects if non-empty. */}
          <input type="text" value={form.website} tabIndex={-1} autoComplete="off"
            onChange={e => setForm({ ...form, website: e.target.value })}
            style={{ position: "absolute", left: "-9999px" }} aria-hidden="true" />

          {status === "error" && (
            <div style={{ fontSize: "12px", color: "#dc2626", marginBottom: "12px" }}>{errorMsg}</div>
          )}

          <button type="submit" disabled={status === "sending"} style={{
            width: "100%", padding: "10px 0", background: "#3b82f6", color: "#fff",
            border: "none", borderRadius: "6px", fontSize: "14px", fontWeight: 700,
            cursor: status === "sending" ? "default" : "pointer", opacity: status === "sending" ? 0.7 : 1,
          }}>
            {status === "sending" ? "Sending…" : "Send message"}
          </button>
        </form>
      </div>
    </div>
  )
}
