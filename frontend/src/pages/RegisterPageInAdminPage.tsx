// frontend/src/pages/RegisterPageInAdminPage.tsx
import { useState, FormEvent, useContext } from "react";
import { useNavigate, Link } from "react-router-dom";
import { useAuth, UserRole } from "../context/AuthContext";


// Add to ROLES array:
const ROLES = [
  { value: "analyst",  label: "Analyst",  desc: "Submit corrections, view extractions",   color: "#60a5fa" },
  { value: "reviewer", label: "Reviewer", desc: "Approve / reject fields, manage queue",  color: "#a78bfa" },
  { value: "admin",    label: "Admin",    desc: "Full access including schema management", color: "#facc15" },
  { value: "debug",    label: "Debug",    desc: "Pipeline and performance monitoring",     color: "#22c55e" }, // ← add
];

type Field = "username" | "email" | "password" | "confirmPassword";
const dark =false;
export default function RegisterPageInAdminPage() {
  const { register } = useAuth();
  const navigate     = useNavigate();

  const [form, setForm] = useState({
    username: "", email: "", password: "", confirmPassword: "",
  });
  const [role,     setRole]     = useState<UserRole>("analyst");
  const [errors,   setErrors]   = useState<Partial<Record<Field, string>>>({});
  const [apiError, setApiError] = useState("");
  const [loading,  setLoading]  = useState(false);
  const [showPw,   setShowPw]   = useState(false);

  // ── Theme tokens ──────────────────────────────────────────────
  const t = {
    bg:          dark ? "#0f172a" : "#f1f5f9",
    text:        dark ? "#e2e8f0" : "#1e293b",
    subtext:     dark ? "#475569" : "#64748b",
    card:        dark ? "#1e293b" : "#ffffff",
    cardBorder:  dark ? "#334155" : "#e2e8f0",
    inputBg:     dark ? "#0f172a" : "#f8fafc",
    inputBorder: dark ? "#334155" : "#e2e8f0",
    sectionTitle:dark ? "#475569" : "#94a3b8",
    roleCard:    dark ? "#0f172a" : "#f8fafc",
    roleHover:   dark ? "#1e293b" : "#f1f5f9",
  }

  const set = (field: Field) => (e: React.ChangeEvent<HTMLInputElement>) => {
    setForm(f => ({ ...f, [field]: e.target.value }));
    setErrors(er => ({ ...er, [field]: undefined }));
    setApiError("");
  };

  const validate = (): boolean => {
    const errs: Partial<Record<Field, string>> = {};
    if (form.username.trim().length < 3)          errs.username = "At least 3 characters";
    if (!/^[a-zA-Z0-9_-]+$/.test(form.username)) errs.username = "Letters, numbers, _ and - only";
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(form.email)) errs.email = "Enter a valid email address";
    if (form.password.length < 8)                 errs.password = "At least 8 characters";
    if (form.password !== form.confirmPassword)   errs.confirmPassword = "Passwords do not match";
    setErrors(errs);
    return Object.keys(errs).length === 0;
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!validate()) return;
    setLoading(true);
    setApiError("");
    try {
      await register(form.username.trim().toLowerCase(), form.email.trim().toLowerCase(), form.password, role);
      // ✅ redirect based on role
      if (role === "reviewer")      navigate("/queue");
      else if (role === "admin")    navigate("/dashboard");
      else if (role === "debug")    navigate("/pipeline1");
      else                          navigate("/upload");
    } catch (err: any) {
      setApiError(err.message || "Registration failed");
    } finally {
      setLoading(false);
    }
  };

  const pwStrength = (pw: string): { label: string; color: string; pct: number } => {
    if (!pw) return { label: "", color: t.inputBorder, pct: 0 };
    let score = 0;
    if (pw.length >= 8)          score++;
    if (pw.length >= 12)         score++;
    if (/[A-Z]/.test(pw))        score++;
    if (/[0-9]/.test(pw))        score++;
    if (/[^A-Za-z0-9]/.test(pw)) score++;
    if (score <= 1) return { label: "Weak",   color: "#ef4444", pct: 25  };
    if (score <= 2) return { label: "Fair",   color: "#f59e0b", pct: 50  };
    if (score <= 3) return { label: "Good",   color: "#3b82f6", pct: 75  };
    return              { label: "Strong", color: "#22c55e", pct: 100 };
  };

  const strength = pwStrength(form.password);

  const inputStyle = (hasError: boolean) => ({
    width: "100%", padding: "10px 14px",
    background: t.inputBg, color: t.text,
    border: `1px solid ${hasError ? "#ef4444" : t.inputBorder}`,
    borderRadius: "8px", fontSize: "14px",
    fontFamily: "'IBM Plex Mono', monospace",
    outline: "none", boxSizing: "border-box" as const,
    transition: "border-color .15s, box-shadow .15s",
  });

  return (
    <div style={{
      minHeight: "100vh", display: "flex", alignItems: "center", justifyContent: "center",
      background: t.bg, fontFamily: "'Sora', sans-serif",
      color: t.text, padding: "32px 20px",
      transition: "background .2s, color .2s",
    }}>
      <div style={{ width: "100%", maxWidth: "460px", display: "flex", flexDirection: "column", gap: "24px" }}>

        {/* ── Brand ── */}
        <div style={{ display: "flex", alignItems: "center", gap: "12px" }}>
          <span style={{ fontSize: "32px", lineHeight: "1" }}>⬡</span>
          <div>
            <div style={{
              fontSize: "20px", fontWeight: 700,
              background: "linear-gradient(135deg, #60a5fa, #a78bfa)",
              WebkitBackgroundClip: "text", WebkitTextFillColor: "transparent",
            }}>
              HITL Extract
            </div>
            <div style={{ fontSize: "11px", color: t.subtext }}>
              Human-in-the-Loop Extraction Platform
            </div>
          </div>
        </div>

        {/* ── Card ── */}
        <div style={{
          background: t.card, border: `1px solid ${t.cardBorder}`,
          borderRadius: "16px", padding: "28px 28px 24px",
          transition: "background .2s",
        }}>
          <h1 style={{ fontSize: "22px", fontWeight: 700, margin: "0 0 4px", color: t.text }}>
            Create account
          </h1>
          <p style={{ fontSize: "13px", color: t.subtext, margin: "0 0 24px" }}>
            Fill in your details to get started
          </p>

          <form onSubmit={handleSubmit} noValidate style={{ display: "flex", flexDirection: "column", gap: "16px" }}>

            {/* Username */}
            <div style={{ display: "flex", flexDirection: "column", gap: "5px" }}>
              <label style={{ fontSize: "11px", fontWeight: 700, textTransform: "uppercase", letterSpacing: ".06em", color: t.sectionTitle }}>
                Username
              </label>
              <input
                type="text" autoComplete="username" autoFocus
                value={form.username} onChange={set("username")}
                placeholder="e.g. john_doe" disabled={loading}
                style={inputStyle(!!errors.username)}
                onFocus={e => e.currentTarget.style.boxShadow = "0 0 0 3px rgba(59,130,246,.15)"}
                onBlur={e  => e.currentTarget.style.boxShadow = "none"}
              />
              {errors.username && <span style={{ fontSize: "11px", color: "#f87171" }}>{errors.username}</span>}
            </div>

            {/* Email */}
            <div style={{ display: "flex", flexDirection: "column", gap: "5px" }}>
              <label style={{ fontSize: "11px", fontWeight: 700, textTransform: "uppercase", letterSpacing: ".06em", color: t.sectionTitle }}>
                Email
              </label>
              <input
                type="email" autoComplete="email"
                value={form.email} onChange={set("email")}
                placeholder="you@example.com" disabled={loading}
                style={inputStyle(!!errors.email)}
                onFocus={e => e.currentTarget.style.boxShadow = "0 0 0 3px rgba(59,130,246,.15)"}
                onBlur={e  => e.currentTarget.style.boxShadow = "none"}
              />
              {errors.email && <span style={{ fontSize: "11px", color: "#f87171" }}>{errors.email}</span>}
            </div>

            {/* Password */}
            <div style={{ display: "flex", flexDirection: "column", gap: "5px" }}>
              <label style={{ fontSize: "11px", fontWeight: 700, textTransform: "uppercase", letterSpacing: ".06em", color: t.sectionTitle }}>
                Password
              </label>
              <div style={{ position: "relative" }}>
                <input
                  type={showPw ? "text" : "password"} autoComplete="new-password"
                  value={form.password} onChange={set("password")}
                  placeholder="Min 8 characters" disabled={loading}
                  style={{ ...inputStyle(!!errors.password), paddingRight: "44px" }}
                  onFocus={e => e.currentTarget.style.boxShadow = "0 0 0 3px rgba(59,130,246,.15)"}
                  onBlur={e  => e.currentTarget.style.boxShadow = "none"}
                />
                <button
                  type="button" tabIndex={-1}
                  onClick={() => setShowPw(v => !v)}
                  style={{
                    position: "absolute", right: 0, top: 0, bottom: 0, width: "44px",
                    background: "none", border: "none", cursor: "pointer",
                    fontSize: "15px", opacity: .5, transition: "opacity .15s",
                    display: "flex", alignItems: "center", justifyContent: "center",
                  }}
                  onMouseEnter={e => e.currentTarget.style.opacity = "1"}
                  onMouseLeave={e => e.currentTarget.style.opacity = ".5"}
                >
                  {showPw ? "🙈" : "👁"}
                </button>
              </div>
              {form.password && (
                <div style={{ display: "flex", alignItems: "center", gap: "8px", marginTop: "2px" }}>
                  <div style={{ flex: 1, height: "3px", background: t.cardBorder, borderRadius: "2px", overflow: "hidden" }}>
                    <div style={{ height: "100%", borderRadius: "2px", width: `${strength.pct}%`, background: strength.color, transition: "width .3s, background .3s" }} />
                  </div>
                  <span style={{ fontSize: "11px", fontWeight: 700, color: strength.color, minWidth: "42px", fontFamily: "'IBM Plex Mono', monospace" }}>
                    {strength.label}
                  </span>
                </div>
              )}
              {errors.password && <span style={{ fontSize: "11px", color: "#f87171" }}>{errors.password}</span>}
            </div>

            {/* Confirm password */}
            <div style={{ display: "flex", flexDirection: "column", gap: "5px" }}>
              <label style={{ fontSize: "11px", fontWeight: 700, textTransform: "uppercase", letterSpacing: ".06em", color: t.sectionTitle }}>
                Confirm password
              </label>
              <input
                type={showPw ? "text" : "password"} autoComplete="new-password"
                value={form.confirmPassword} onChange={set("confirmPassword")}
                placeholder="Repeat password" disabled={loading}
                style={inputStyle(!!errors.confirmPassword)}
                onFocus={e => e.currentTarget.style.boxShadow = "0 0 0 3px rgba(59,130,246,.15)"}
                onBlur={e  => e.currentTarget.style.boxShadow = "none"}
              />
              {errors.confirmPassword && <span style={{ fontSize: "11px", color: "#f87171" }}>{errors.confirmPassword}</span>}
            </div>

            {/* Role selector */}
            <div style={{ display: "flex", flexDirection: "column", gap: "5px" }}>
              <label style={{ fontSize: "11px", fontWeight: 700, textTransform: "uppercase", letterSpacing: ".06em", color: t.sectionTitle }}>
                Role
              </label>
              <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
                {ROLES.map(r => {
                  const active = role === r.value;
                  return (
                    <div
                      key={r.value}
                      onClick={() => setRole(r.value)}
                      style={{
                        display: "flex", alignItems: "center", gap: "10px",
                        padding: "10px 12px", borderRadius: "10px", cursor: "pointer",
                        border: `1px solid ${active ? r.color + "66" : t.cardBorder}`,
                        background: active ? r.color + "12" : t.roleCard,
                        transition: "all .15s", position: "relative",
                      }}
                      onMouseEnter={e => { if (!active) e.currentTarget.style.background = t.roleHover }}
                      onMouseLeave={e => { if (!active) e.currentTarget.style.background = active ? r.color + "12" : t.roleCard }}
                    >
                      {active && (
                        <div style={{ position: "absolute", left: 0, top: 0, bottom: 0, width: "3px", borderRadius: "3px 0 0 3px", background: r.color }} />
                      )}
                      <div style={{ width: "8px", height: "8px", borderRadius: "50%", background: r.color, flexShrink: 0 }} />
                      <div style={{ flex: 1 }}>
                        <div style={{ fontSize: "13px", fontWeight: 600, color: active ? r.color : t.text }}>
                          {r.label}
                        </div>
                        <div style={{ fontSize: "11px", color: t.subtext, marginTop: "1px" }}>
                          {r.desc}
                        </div>
                      </div>
                      {active && <span style={{ fontSize: "13px", color: r.color, fontWeight: 700 }}>✓</span>}
                    </div>
                  );
                })}
              </div>
            </div>

            {/* API error */}
            {apiError && (
              <div style={{
                display: "flex", alignItems: "center", gap: "8px",
                background: dark ? "rgba(239,68,68,.08)" : "rgba(239,68,68,.04)",
                border: "1px solid rgba(239,68,68,.3)",
                borderRadius: "8px", padding: "10px 14px",
                fontSize: "12px", color: "#f87171",
              }}>
                <span style={{
                  fontSize: "10px", width: "18px", height: "18px", borderRadius: "50%",
                  background: "rgba(239,68,68,.2)", display: "flex",
                  alignItems: "center", justifyContent: "center", fontWeight: 700, flexShrink: 0,
                }}>✕</span>
                {apiError}
              </div>
            )}

            {/* Submit */}
            <button
              type="submit" disabled={loading}
              style={{
                background: "linear-gradient(135deg, #2563eb, #1d4ed8)",
                border: "none", borderRadius: "10px", color: "white",
                fontFamily: "'Sora', sans-serif", fontSize: "14px", fontWeight: 700,
                padding: "11px", cursor: loading ? "default" : "pointer",
                display: "flex", alignItems: "center", justifyContent: "center", gap: "8px",
                boxShadow: "0 4px 16px rgba(37,99,235,.3)", transition: "all .15s",
                opacity: loading ? 0.5 : 1, marginTop: "4px",
              }}
            >
              {loading && (
                <span style={{
                  width: "14px", height: "14px", borderRadius: "50%",
                  border: "2px solid rgba(255,255,255,.3)", borderTopColor: "white",
                  animation: "spin .7s linear infinite", display: "inline-block",
                }} />
              )}
              {loading ? "Creating account…" : "Create account →"}
            </button>

            <p style={{ textAlign: "center", fontSize: "13px", color: t.subtext, margin: 0 }}>
              Already have an account?{" "}
              <Link to="/login" style={{ color: "#60a5fa", textDecoration: "none", fontWeight: 600 }}>
                Sign in
              </Link>
            </p>

          </form>
        </div>
      </div>

      <style>{`
        @import url('https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;600&family=Sora:wght@400;600;700&display=swap');
        @keyframes spin { to { transform: rotate(360deg); } }
      `}</style>
    </div>
  );
}