// frontend/src/pages/AccountsPage.tsx
import { useState, useEffect } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { ownerListUsersRaw, ownerToggleUserRaw } from "../lib/api";

// ── Types ─────────────────────────────────────────────────────────────────
interface ApiUser {
  user_id:    string;
  username:   string;
  email:      string;
  role:       string;
  is_active:  boolean;
  created_at: string;
  last_login: string | null;
}

// ── Helpers ───────────────────────────────────────────────────────────────
function fmt(iso: string | null) {
  if (!iso) return "Never";
  return new Date(iso).toLocaleDateString("en-IN", {
    day: "2-digit", month: "short", year: "numeric",
  });
}

function initials(username: string) {
  const parts = username.split(/[\s_\-]+/);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  return username.slice(0, 2).toUpperCase();
}

const AVATAR_COLORS: Record<string, { bg: string; text: string }> = {
  A:{ bg:"#1e3a5f", text:"#60a5fa" }, B:{ bg:"#1e3a5f", text:"#60a5fa" },
  C:{ bg:"#1a2e1a", text:"#4ade80" }, D:{ bg:"#1a2e1a", text:"#4ade80" },
  E:{ bg:"#2d1b1b", text:"#f87171" }, F:{ bg:"#2d1b1b", text:"#f87171" },
  G:{ bg:"#2a1f3d", text:"#a78bfa" }, H:{ bg:"#2a1f3d", text:"#a78bfa" },
  I:{ bg:"#1e2d3a", text:"#38bdf8" }, J:{ bg:"#1e2d3a", text:"#38bdf8" },
  K:{ bg:"#2d2010", text:"#fb923c" }, L:{ bg:"#2d2010", text:"#fb923c" },
  M:{ bg:"#1e3a5f", text:"#60a5fa" }, N:{ bg:"#1a2e1a", text:"#4ade80" },
  O:{ bg:"#2d1b1b", text:"#f87171" }, P:{ bg:"#2a1f3d", text:"#a78bfa" },
  Q:{ bg:"#1e2d3a", text:"#38bdf8" }, R:{ bg:"#2d2010", text:"#fb923c" },
  S:{ bg:"#1e3a5f", text:"#60a5fa" }, T:{ bg:"#1a2e1a", text:"#4ade80" },
  U:{ bg:"#2d1b1b", text:"#f87171" }, V:{ bg:"#2a1f3d", text:"#a78bfa" },
  W:{ bg:"#1e2d3a", text:"#38bdf8" }, X:{ bg:"#2d2010", text:"#fb923c" },
  Y:{ bg:"#1e3a5f", text:"#60a5fa" }, Z:{ bg:"#1a2e1a", text:"#4ade80" },
};

function avatarColor(name: string) {
  const k = (name[0] ?? "A").toUpperCase();
  return AVATAR_COLORS[k] ?? { bg: "#1e293b", text: "#94a3b8" };
}

// ── Confirm Dialog ────────────────────────────────────────────────────────
function ConfirmDialog({
  user, action, onConfirm, onCancel, isPending,
}: {
  user: ApiUser;
  action: "activate" | "deactivate";
  onConfirm: () => void;
  onCancel: () => void;
  isPending: boolean;
}) {
  const ac = avatarColor(user.username);
  const av = initials(user.username);
  return (
    <div style={{
      position: "fixed", inset: 0, zIndex: 100,
      background: "rgba(0,0,0,.75)", backdropFilter: "blur(4px)",
      display: "flex", alignItems: "center", justifyContent: "center",
    }}>
      <div style={{
        background: "#0d1b2e", border: "1px solid rgba(59,130,246,.25)",
        borderRadius: "16px", padding: "32px 28px", width: "340px",
        fontFamily: "'DM Sans', sans-serif", animation: "popIn .2s ease",
      }}>
        <div style={{ display: "flex", alignItems: "center", gap: "14px", marginBottom: "20px" }}>
          <div style={{
            width: "44px", height: "44px", borderRadius: "12px",
            background: ac.bg, color: ac.text, flexShrink: 0,
            display: "flex", alignItems: "center", justifyContent: "center",
            fontSize: "14px", fontWeight: 700, fontFamily: "'DM Mono', monospace",
          }}>{av}</div>
          <div>
            <div style={{ fontSize: "14px", fontWeight: 700, color: "#e2e8f0" }}>{user.username}</div>
            <div style={{ fontSize: "12px", color: "#475569" }}>{user.email}</div>
          </div>
        </div>
        <p style={{ fontSize: "13px", color: "#94a3b8", margin: "0 0 24px", lineHeight: 1.6 }}>
          {action === "deactivate"
            ? "This will revoke login access immediately. The user will be blocked on their next request."
            : "This will restore login access for this user immediately."}
        </p>
        <div style={{ display: "flex", gap: "10px" }}>
          <button
            onClick={onCancel}
            disabled={isPending}
            style={{
              flex: 1, background: "rgba(255,255,255,.05)",
              border: "1px solid rgba(255,255,255,.1)",
              borderRadius: "8px", color: "#94a3b8", fontSize: "12px",
              fontWeight: 600, fontFamily: "'DM Mono', monospace",
              padding: "10px", cursor: "pointer",
            }}
          >CANCEL</button>
          <button
            onClick={onConfirm}
            disabled={isPending}
            style={{
              flex: 1, border: "none", borderRadius: "8px",
              fontSize: "12px", fontWeight: 700,
              fontFamily: "'DM Mono', monospace", padding: "10px",
              cursor: isPending ? "default" : "pointer",
              opacity: isPending ? 0.6 : 1,
              background: action === "deactivate"
                ? "linear-gradient(135deg,#b91c1c,#dc2626)"
                : "linear-gradient(135deg,#15803d,#16a34a)",
              color: "white",
            }}
          >
            {isPending ? "..." : action === "deactivate" ? "DEACTIVATE" : "ACTIVATE"}
          </button>
        </div>
      </div>
      <style>{`@keyframes popIn{from{opacity:0;transform:scale(.92)}to{opacity:1;transform:scale(1)}}`}</style>
    </div>
  );
}

// ── Login Screen ──────────────────────────────────────────────────────────
function LoginScreen({ onLogin }: { onLogin: () => void }) {
  const navigate = useNavigate();
  const [username, setUsername] = useState("");
  const [pass,      setPass]     = useState("");
  const [err,       setErr]      = useState("");
  const [shake,     setShake]    = useState(false);
  const [checking,  setChecking] = useState(false);

  // Always redirect to /accounts after successful auth
  function loginAndRedirect() {
    onLogin();
    navigate("/accounts", { replace: true });
  }

  function triggerError(msg: string) {
    setErr(msg);
    setShake(true);
    setTimeout(() => setShake(false), 500);
  }

  // Verifies the entered credentials against the real server-side check
  // (hitl-go/routers/account.go's basicAuth middleware, timing-safe
  // compare against OWNER_USERNAME/OWNER_PASSWORD) rather than comparing
  // against anything baked into this bundle. Uses ownerListUsersRaw — not
  // apiFetch — so a wrong owner password here never clears the logged-in
  // user's own session (apiFetch's global 401 handler would do exactly
  // that, since it doesn't distinguish "owner auth failed" from "your
  // user session expired").
  async function attemptLogin() {
    if (checking) return;
    setChecking(true);
    setErr("");
    const header = "Basic " + btoa(`${username}:${pass}`);
    try {
      const res = await ownerListUsersRaw(header);
      if (res.status === 401) {
        triggerError("Incorrect owner username or password.");
        return;
      }
      if (!res.ok) {
        triggerError(`Server error (HTTP ${res.status}). Please try again.`);
        return;
      }
      // Only store the header once the server has actually accepted it.
      sessionStorage.setItem("owner_basic_auth", header);
      sessionStorage.setItem("owner_username", username);
      loginAndRedirect();
    } catch {
      triggerError("Could not reach the server. Please try again.");
    } finally {
      setChecking(false);
    }
  }

  return (
    <div style={{
      minHeight: "100vh", background: "#060d1a",
      display: "flex", alignItems: "center", justifyContent: "center",
      fontFamily: "'DM Mono', monospace", position: "relative", overflow: "hidden",
    }}>
      {/* Grid bg */}
      <div style={{
        position: "absolute", inset: 0, pointerEvents: "none",
        backgroundImage: `
          linear-gradient(rgba(59,130,246,.04) 1px, transparent 1px),
          linear-gradient(90deg,rgba(59,130,246,.04) 1px,transparent 1px)
        `,
        backgroundSize: "48px 48px",
      }}/>
      {/* Glow */}
      <div style={{
        position: "absolute", top: "50%", left: "50%",
        transform: "translate(-50%,-50%)",
        width: "600px", height: "600px",
        background: "radial-gradient(circle,rgba(37,99,235,.1) 0%,transparent 70%)",
        pointerEvents: "none",
      }}/>

      <div style={{
        position: "relative", zIndex: 1, width: "380px",
        border: "1px solid rgba(59,130,246,.2)", borderRadius: "16px",
        background: "rgba(10,20,40,.88)", backdropFilter: "blur(12px)",
        padding: "36px", animation: shake ? "shake .4s ease" : "fadeIn .4s ease",
      }}>
        {/* Icon */}
        <div style={{
          width: "52px", height: "52px", margin: "0 auto 20px",
          background: "rgba(37,99,235,.15)", border: "1px solid rgba(59,130,246,.3)",
          borderRadius: "14px", display: "flex", alignItems: "center",
          justifyContent: "center", fontSize: "22px",
        }}>🔐</div>

        <h1 style={{
          textAlign: "center", fontSize: "20px", fontWeight: 700,
          color: "#e2e8f0", margin: "0 0 4px",
        }}>Owner Access</h1>
        <p style={{
          textAlign: "center", fontSize: "11px", color: "#475569",
          margin: "0 0 24px", letterSpacing: ".05em",
        }}>ACCOUNTS MANAGEMENT PORTAL</p>

        <label style={{ display: "block", fontSize: "10px", color: "#475569", letterSpacing: ".06em", marginBottom: "5px" }}>
          OWNER USERNAME
        </label>
        <input
          value={username}
          onChange={e => { setUsername(e.target.value); setErr(""); }}
          onKeyDown={e => e.key === "Enter" && attemptLogin()}
          placeholder="owner"
          style={{
            display: "block", width: "100%", boxSizing: "border-box",
            background: "rgba(255,255,255,.04)", border: "1px solid rgba(59,130,246,.2)",
            borderRadius: "8px", padding: "10px 14px",
            color: "#e2e8f0", fontSize: "13px", fontFamily: "'DM Mono', monospace",
            outline: "none", marginBottom: "14px", transition: "border-color .15s",
          }}
          onFocus={e => (e.target.style.borderColor = "rgba(59,130,246,.6)")}
          onBlur={e  => (e.target.style.borderColor = "rgba(59,130,246,.2)")}
        />

        <label style={{ display: "block", fontSize: "10px", color: "#475569", letterSpacing: ".06em", marginBottom: "5px" }}>
          PASSWORD
        </label>
        <input
          type="password"
          value={pass}
          onChange={e => { setPass(e.target.value); setErr(""); }}
          onKeyDown={e => e.key === "Enter" && attemptLogin()}
          placeholder="••••••••"
          style={{
            display: "block", width: "100%", boxSizing: "border-box",
            background: "rgba(255,255,255,.04)", border: "1px solid rgba(59,130,246,.2)",
            borderRadius: "8px", padding: "10px 14px",
            color: "#e2e8f0", fontSize: "13px", fontFamily: "'DM Mono', monospace",
            outline: "none", marginBottom: "8px", transition: "border-color .15s",
          }}
          onFocus={e => (e.target.style.borderColor = "rgba(59,130,246,.6)")}
          onBlur={e  => (e.target.style.borderColor = "rgba(59,130,246,.2)")}
        />

        {err && (
          <p style={{
            fontSize: "11px", color: "#f87171", margin: "0 0 14px",
            padding: "8px 12px", background: "rgba(239,68,68,.08)",
            border: "1px solid rgba(239,68,68,.2)", borderRadius: "6px",
          }}>{err}</p>
        )}
        {!err && <div style={{ height: "14px" }}/>}

        <button
          onClick={attemptLogin}
          disabled={checking}
          style={{
            display: "block", width: "100%",
            background: "linear-gradient(135deg,#1d4ed8,#2563eb)",
            border: "none", borderRadius: "8px", color: "white",
            fontSize: "13px", fontWeight: 700,
            fontFamily: "'DM Mono', monospace", letterSpacing: ".04em",
            padding: "12px", cursor: checking ? "default" : "pointer",
            boxShadow: "0 4px 20px rgba(37,99,235,.35)", transition: "opacity .15s",
            opacity: checking ? 0.7 : 1,
          }}
          onMouseEnter={e => { if (!checking) e.currentTarget.style.opacity = ".85"; }}
          onMouseLeave={e => { if (!checking) e.currentTarget.style.opacity = "1"; }}
        >
          {checking ? "CHECKING…" : "AUTHENTICATE →"}
        </button>
      </div>

      <style>{`
        @import url('https://fonts.googleapis.com/css2?family=DM+Mono:wght@400;500&family=DM+Sans:wght@400;500;600;700&display=swap');
        @keyframes fadeIn { from{opacity:0;transform:translateY(12px)} to{opacity:1;transform:translateY(0)} }
        @keyframes shake  { 0%,100%{transform:translateX(0)} 20%,60%{transform:translateX(-8px)} 40%,80%{transform:translateX(8px)} }
        @keyframes spin   { to{transform:rotate(360deg)} }
      `}</style>
    </div>
  );
}

// ── Dashboard ─────────────────────────────────────────────────────────────
function AccountsDashboard({ onLogout }: { onLogout: () => void }) {
  const qc = useQueryClient();

  const [search,  setSearch]  = useState("");
  const [filter,  setFilter]  = useState<"all" | "active" | "inactive">("all");
  const [confirm, setConfirm] = useState<{ user: ApiUser; action: "activate" | "deactivate" } | null>(null);
  const [toast,   setToast]   = useState<string | null>(null);
  const [dark,    setDark]    = useState(() => {
    const saved = localStorage.getItem("accounts_theme");
    return saved !== null ? saved === "dark" : true; // default dark
  });

  function toggleTheme() {
    setDark(prev => {
      const next = !prev;
      localStorage.setItem("accounts_theme", next ? "dark" : "light");
      return next;
    });
  }

  // ── Theme tokens ──────────────────────────────────────────────────────
  const t = {
    pageBg:          dark ? "#060d1a"                : "#f1f5f9",
    gridLine:        dark ? "rgba(59,130,246,.03)"   : "rgba(59,130,246,.06)",
    navBg:           dark ? "rgba(6,13,26,.92)"      : "rgba(255,255,255,.92)",
    navBorder:       dark ? "rgba(59,130,246,.12)"   : "rgba(59,130,246,.15)",
    text:            dark ? "#e2e8f0"                : "#1e293b",
    subtext:         dark ? "#475569"                : "#64748b",
    dimtext:         dark ? "#94a3b8"                : "#475569",
    card:            dark ? "rgba(255,255,255,.02)"  : "#ffffff",
    cardBorder:      dark ? "rgba(59,130,246,.07)"   : "#e2e8f0",
    cardHover:       dark ? "rgba(59,130,246,.06)"   : "rgba(59,130,246,.04)",
    cardHoverBorder: dark ? "rgba(59,130,246,.2)"    : "rgba(59,130,246,.3)",
    inputBg:         dark ? "rgba(255,255,255,.04)"  : "#ffffff",
    inputBorder:     dark ? "rgba(59,130,246,.15)"   : "#e2e8f0",
    filterBtn:       dark ? "rgba(255,255,255,.04)"  : "#ffffff",
    filterBtnBorder: dark ? "rgba(255,255,255,.1)"   : "#e2e8f0",
    tableHead:       dark ? "#334155"                : "#94a3b8",
    tableBorder:     dark ? "rgba(59,130,246,.1)"    : "#e2e8f0",
    skeletonBg:      dark ? "rgba(255,255,255,.05)"  : "#f1f5f9",
    toastBg:         dark ? "#0d1b2e"                : "#ffffff",
    toastBorder:     dark ? "rgba(59,130,246,.3)"    : "#e2e8f0",
    toastText:       dark ? "#e2e8f0"                : "#1e293b",
    warningBg:       dark ? "rgba(234,179,8,.06)"    : "rgba(234,179,8,.08)",
    warningBorder:   dark ? "rgba(234,179,8,.15)"    : "rgba(234,179,8,.3)",
    warningText:     dark ? "#a16207"                : "#92400e",
    errorBg:         dark ? "rgba(239,68,68,.06)"    : "rgba(239,68,68,.04)",
    errorBorder:     dark ? "rgba(239,68,68,.2)"     : "rgba(239,68,68,.3)",
  };

  const ownerUsername = sessionStorage.getItem("owner_username") ?? "";
  const ownerAuthHeader = sessionStorage.getItem("owner_basic_auth") ?? "";

  function showToast(msg: string) {
    setToast(msg);
    setTimeout(() => setToast(null), 2800);
  }

  // Clears only the owner session (not the app's regular hitl_auth JWT —
  // these are deliberately separate credentials) and sends back to login.
  function ownerSessionExpired() {
    sessionStorage.removeItem("owner_basic_auth");
    sessionStorage.removeItem("owner_username");
    window.location.href = "/accounts/login";
  }

  // ── Fetch users ──────────────────────────────────────────────────────
  const usersQuery = useQuery<{ total: number; active: number; users: ApiUser[] }>({
    queryKey:        ["account-users"],
    queryFn:         async () => {
      const res = await ownerListUsersRaw(ownerAuthHeader);
      if (res.status === 401) {
        ownerSessionExpired();
        throw new Error("Owner session expired");
      }
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        throw new Error(body.error || `HTTP ${res.status}`);
      }
      return res.json();
    },
    refetchInterval: 15000,
  });

  // ── Toggle mutation ──────────────────────────────────────────────────
  const toggleMutation = useMutation({
    mutationFn: async (userId: string) => {
      const res = await ownerToggleUserRaw(userId, ownerAuthHeader);
      if (res.status === 401) {
        ownerSessionExpired();
        throw new Error("Owner session expired");
      }
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        throw new Error(body.error || `HTTP ${res.status}`);
      }
      return res.json();
    },
    onSuccess: (data: any) => {
      qc.invalidateQueries({ queryKey: ["account-users"] });
      showToast(
        data.action === "deactivated"
          ? `${data.username}'s account has been deactivated.`
          : `${data.username}'s account is now active.`
      );
      setConfirm(null);
    },
    onError: () => {
      showToast("Something went wrong. Please try again.");
      setConfirm(null);
    },
  });

  const allUsers  = usersQuery.data?.users ?? [];
  const total     = usersQuery.data?.total ?? 0;
  const active    = usersQuery.data?.active ?? 0;
  const inactive  = total - active;

  const filtered = allUsers.filter(u => {
    const q = search.toLowerCase();
    const matchSearch =
      u.username.toLowerCase().includes(q) ||
      u.email.toLowerCase().includes(q)    ||
      u.role.toLowerCase().includes(q);
    const matchFilter =
      filter === "all"      ? true :
      filter === "active"   ? u.is_active : !u.is_active;
    return matchSearch && matchFilter;
  });

  const roleColors: Record<string, { bg: string; text: string }> = {
    manager:  { bg: "rgba(124,58,237,.15)", text: "#a78bfa" },
    admin:    { bg: "rgba(239,68,68,.12)",  text: "#f87171" },
    reviewer: { bg: "rgba(59,130,246,.15)", text: "#60a5fa" },
    analyst:  { bg: "rgba(20,184,166,.15)", text: "#2dd4bf" },
  };

  return (
    <div style={{
      minHeight: "100vh", background: t.pageBg,
      fontFamily: "'DM Sans', sans-serif", color: t.text,
      transition: "background .2s, color .2s",
    }}>
      {/* Grid bg */}
      <div style={{
        position: "fixed", inset: 0, pointerEvents: "none",
        backgroundImage: `
          linear-gradient(${t.gridLine} 1px, transparent 1px),
          linear-gradient(90deg,${t.gridLine} 1px,transparent 1px)
        `,
        backgroundSize: "64px 64px",
      }}/>

      {/* ── Top bar ── */}
      <div style={{
        position: "sticky", top: 0, zIndex: 10,
        background: t.navBg, backdropFilter: "blur(12px)",
        borderBottom: `1px solid ${t.navBorder}`,
        padding: "0 32px", height: "56px",
        display: "flex", alignItems: "center", justifyContent: "space-between",
      }}>
        <div style={{ display: "flex", alignItems: "center", gap: "12px" }}>
          <div style={{
            width: "32px", height: "32px", borderRadius: "8px",
            background: "rgba(37,99,235,.2)", border: "1px solid rgba(59,130,246,.3)",
            display: "flex", alignItems: "center", justifyContent: "center", fontSize: "14px",
          }}>⚙</div>
          <span style={{ fontSize: "15px", fontWeight: 700, color: t.text }}>Accounts</span>
          <span style={{
            fontSize: "10px", fontWeight: 700, letterSpacing: ".08em",
            background: "rgba(37,99,235,.2)", color: "#60a5fa",
            border: "1px solid rgba(59,130,246,.3)",
            borderRadius: "6px", padding: "2px 8px",
            fontFamily: "'DM Mono', monospace",
          }}>OWNER</span>
        </div>

        {/* Owner info + logout */}
        <div style={{ display: "flex", alignItems: "center", gap: "14px" }}>
          <div style={{ textAlign: "right" }}>
            <div style={{ fontSize: "11px", color: "#475569", fontFamily: "'DM Mono', monospace" }}>
              🔐 Owner
            </div>
            <div style={{ fontSize: "12px", color: t.dimtext }}>{ownerUsername}</div>
          </div>
          {/* Theme toggle */}
          <button
            onClick={toggleTheme}
            title={dark ? "Switch to light mode" : "Switch to dark mode"}
            style={{
              background: dark ? "rgba(255,255,255,.06)" : "rgba(0,0,0,.06)",
              border: `1px solid ${dark ? "rgba(255,255,255,.12)" : "rgba(0,0,0,.1)"}`,
              borderRadius: "8px", color: dark ? "#94a3b8" : "#64748b",
              fontSize: "16px", padding: "5px 10px", cursor: "pointer",
              transition: "all .15s", lineHeight: 1,
              display: "flex", alignItems: "center", justifyContent: "center",
            }}
            onMouseEnter={e => { e.currentTarget.style.background = dark ? "rgba(255,255,255,.12)" : "rgba(0,0,0,.1)"; }}
            onMouseLeave={e => { e.currentTarget.style.background = dark ? "rgba(255,255,255,.06)" : "rgba(0,0,0,.06)"; }}
          >
            {dark ? "☀️" : "🌙"}
          </button>

          <button
            onClick={onLogout}
            style={{
              background: "rgba(239,68,68,.1)", border: "1px solid rgba(239,68,68,.25)",
              borderRadius: "8px", color: "#f87171", fontSize: "11px", fontWeight: 700,
              fontFamily: "'DM Mono', monospace", letterSpacing: ".04em",
              padding: "6px 14px", cursor: "pointer", transition: "all .15s",
            }}
            onMouseEnter={e => { e.currentTarget.style.background = "rgba(239,68,68,.2)"; }}
            onMouseLeave={e => { e.currentTarget.style.background = "rgba(239,68,68,.1)"; }}
          >
            SIGN OUT
          </button>
        </div>
      </div>

      <div style={{ position: "relative", zIndex: 1, padding: "32px", maxWidth: "1100px", margin: "0 auto" }}>

        {/* Title */}
        <div style={{ marginBottom: "28px" }}>
          <h1 style={{
            fontSize: "28px", fontWeight: 700, margin: "0 0 4px",
            background: "linear-gradient(135deg,#60a5fa 0%,#818cf8 100%)",
            WebkitBackgroundClip: "text", WebkitTextFillColor: "transparent",
          }}>User Accounts</h1>
          <p style={{ fontSize: "13px", color: "#475569", margin: 0 }}>
            Manage access and permissions for all registered users
          </p>
        </div>

        {/* Stat cards */}
        <div style={{ display: "grid", gridTemplateColumns: "repeat(3,1fr)", gap: "14px", marginBottom: "28px" }}>
          {[
            { label: "Total Users", value: total,    color: "#60a5fa", bg: "rgba(59,130,246,.1)",  border: "rgba(59,130,246,.2)" },
            { label: "Active",      value: active,   color: "#4ade80", bg: "rgba(34,197,94,.08)",  border: "rgba(34,197,94,.2)"  },
            { label: "Deactivated", value: inactive, color: "#f87171", bg: "rgba(239,68,68,.08)",  border: "rgba(239,68,68,.2)"  },
          ].map(s => (
            <div key={s.label} style={{
              background: s.bg, border: `1px solid ${s.border}`,
              borderRadius: "12px", padding: "18px 20px",
              display: "flex", flexDirection: "column", gap: "4px",
            }}>
              <span style={{ fontSize: "11px", color: t.subtext, letterSpacing: ".06em", textTransform: "uppercase", fontFamily: "'DM Mono', monospace" }}>
                {s.label}
              </span>
              {usersQuery.isLoading
                ? <div style={{ width: "40px", height: "36px", background: t.skeletonBg, borderRadius: "6px", animation: "pulse 1.5s infinite" }}/>
                : <span style={{ fontSize: "32px", fontWeight: 700, color: s.color, fontFamily: "'DM Mono', monospace", lineHeight: 1.1 }}>{s.value}</span>
              }
            </div>
          ))}
        </div>

        {/* Search + filter */}
        <div style={{ display: "flex", gap: "12px", marginBottom: "20px", flexWrap: "wrap" }}>
          <div style={{ position: "relative", flex: 1, minWidth: "200px" }}>
            <span style={{ position: "absolute", left: "12px", top: "50%", transform: "translateY(-50%)", fontSize: "14px", color: "#475569" }}>🔍</span>
            <input
              value={search}
              onChange={e => setSearch(e.target.value)}
              placeholder="Search by name, email or role…"
              style={{
                width: "100%", boxSizing: "border-box",
                background: t.inputBg, border: `1px solid ${t.inputBorder}`,
                borderRadius: "8px", padding: "9px 12px 9px 36px",
                color: t.text, fontSize: "13px", fontFamily: "'DM Sans', sans-serif",
                outline: "none", transition: "border-color .15s",
              }}
              onFocus={e => (e.target.style.borderColor = "rgba(59,130,246,.5)")}
              onBlur={e  => (e.target.style.borderColor = "rgba(59,130,246,.15)")}
            />
          </div>
          {(["all", "active", "inactive"] as const).map(f => (
            <button
              key={f}
              onClick={() => setFilter(f)}
              style={{
                background: filter === f ? "rgba(37,99,235,.25)" : t.filterBtn,
                border: `1px solid ${filter === f ? "rgba(59,130,246,.5)" : t.filterBtnBorder}`,
                borderRadius: "8px",
                color: filter === f ? "#60a5fa" : t.subtext,
                fontSize: "11px", fontWeight: 700, letterSpacing: ".05em",
                fontFamily: "'DM Mono', monospace",
                padding: "9px 16px", cursor: "pointer", transition: "all .15s",
                textTransform: "uppercase",
              }}
            >
              {f === "all" ? `All (${total})` : f === "active" ? `Active (${active})` : `Inactive (${inactive})`}
            </button>
          ))}
        </div>

        {/* Table header */}
        <div style={{
          display: "grid",
          gridTemplateColumns: "2fr 2fr 1fr 1fr 1fr 120px",
          gap: "12px", padding: "8px 16px",
          fontSize: "10px", fontWeight: 700, letterSpacing: ".07em",
          color: t.tableHead, textTransform: "uppercase",
          fontFamily: "'DM Mono', monospace",
          borderBottom: `1px solid ${t.tableBorder}`,
          marginBottom: "6px",
        }}>
          <span>User</span><span>Email</span><span>Role</span>
          <span>Joined</span><span>Status</span>
          <span style={{ textAlign: "right" }}>Action</span>
        </div>

        {/* Loading skeleton */}
        {usersQuery.isLoading && (
          <div style={{ display: "flex", flexDirection: "column", gap: "4px" }}>
            {[...Array(5)].map((_, i) => (
              <div key={i} style={{
                height: "58px", borderRadius: "10px",
                background: t.skeletonBg,
                border: `1px solid ${t.cardBorder}`,
                animation: "pulse 1.5s infinite",
                animationDelay: `${i * 80}ms`,
              }}/>
            ))}
          </div>
        )}

        {/* Error */}
        {usersQuery.isError && (
          <div style={{
            padding: "32px", textAlign: "center",
            color: "#f87171", fontSize: "13px",
            background: t.errorBg,
            border: `1px solid ${t.errorBorder}`,
            borderRadius: "10px",
          }}>
            Failed to load users. Check that the backend is running.
          </div>
        )}

        {/* Empty */}
        {!usersQuery.isLoading && !usersQuery.isError && filtered.length === 0 && (
          <div style={{ padding: "48px", textAlign: "center", color: "#334155", fontSize: "13px" }}>
            No users match your search
          </div>
        )}

        {/* User rows */}
        <div style={{ display: "flex", flexDirection: "column", gap: "4px" }}>
          {filtered.map((user, i) => {
            const av = initials(user.username);
            const ac = avatarColor(user.username);
            const rc = roleColors[user.role.toLowerCase()] ?? { bg: "rgba(100,116,139,.1)", text: "#94a3b8" };

            return (
              <div
                key={user.user_id}
                style={{
                  display: "grid",
                  gridTemplateColumns: "2fr 2fr 1fr 1fr 1fr 120px",
                  gap: "12px", alignItems: "center",
                  padding: "12px 16px",
                  background: t.card,
                  border: `1px solid ${t.cardBorder}`,
                  borderRadius: "10px", transition: "all .15s",
                  animation: `rowIn .3s ease ${i * 40}ms both`,
                  opacity: user.is_active ? 1 : .55,
                }}
                onMouseEnter={e => {
                  e.currentTarget.style.background   = t.cardHover;
                  e.currentTarget.style.borderColor  = t.cardHoverBorder;
                }}
                onMouseLeave={e => {
                  e.currentTarget.style.background  = t.card;
                  e.currentTarget.style.borderColor = t.cardBorder;
                }}
              >
                {/* Avatar + username */}
                <div style={{ display: "flex", alignItems: "center", gap: "10px", minWidth: 0 }}>
                  <div style={{
                    width: "34px", height: "34px", borderRadius: "9px", flexShrink: 0,
                    background: ac.bg, color: ac.text,
                    display: "flex", alignItems: "center", justifyContent: "center",
                    fontSize: "11px", fontWeight: 700, fontFamily: "'DM Mono', monospace",
                  }}>{av}</div>
                  <span style={{
                    fontSize: "13px", fontWeight: 600, color: t.text,
                    overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
                  }}>{user.username}</span>
                </div>

                {/* Email */}
                <span style={{
                  fontSize: "12px", color: t.subtext, fontFamily: "'DM Mono', monospace",
                  overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
                }}>{user.email}</span>

                {/* Role */}
                <span style={{
                  display: "inline-block",
                  background: rc.bg, color: rc.text,
                  borderRadius: "6px", padding: "3px 8px",
                  fontSize: "10px", fontWeight: 700,
                  fontFamily: "'DM Mono', monospace", textAlign: "center",
                }}>{user.role}</span>

                {/* Joined */}
                <span style={{ fontSize: "11px", color: "#475569", fontFamily: "'DM Mono', monospace" }}>
                  {fmt(user.created_at)}
                </span>

                {/* Status */}
                <div style={{ display: "flex", alignItems: "center", gap: "6px" }}>
                  <div style={{
                    width: "7px", height: "7px", borderRadius: "50%",
                    background: user.is_active ? "#4ade80" : "#ef4444",
                    boxShadow: user.is_active ? "0 0 6px rgba(74,222,128,.6)" : "none",
                  }}/>
                  <span style={{
                    fontSize: "11px", fontWeight: 600,
                    fontFamily: "'DM Mono', monospace",
                    color: user.is_active ? "#4ade80" : "#ef4444",
                  }}>
                    {user.is_active ? "Active" : "Inactive"}
                  </span>
                </div>

                {/* Action */}
                <div style={{ display: "flex", justifyContent: "flex-end" }}>
                  <button
                    onClick={() => setConfirm({
                      user,
                      action: user.is_active ? "deactivate" : "activate",
                    })}
                    style={{
                      border: "1px solid", borderRadius: "7px",
                      fontSize: "10px", fontWeight: 700,
                      fontFamily: "'DM Mono', monospace", letterSpacing: ".04em",
                      padding: "6px 12px", cursor: "pointer", transition: "all .15s",
                      ...(user.is_active
                        ? { background: "rgba(239,68,68,.08)", borderColor: "rgba(239,68,68,.3)", color: "#f87171" }
                        : { background: "rgba(34,197,94,.08)",  borderColor: "rgba(34,197,94,.3)",  color: "#4ade80" }
                      ),
                    }}
                    onMouseEnter={e => {
                      e.currentTarget.style.background = user.is_active ? "rgba(239,68,68,.18)" : "rgba(34,197,94,.18)";
                    }}
                    onMouseLeave={e => {
                      e.currentTarget.style.background = user.is_active ? "rgba(239,68,68,.08)" : "rgba(34,197,94,.08)";
                    }}
                  >
                    {user.is_active ? "DEACTIVATE" : "ACTIVATE"}
                  </button>
                </div>
              </div>
            );
          })}
        </div>

        {/* Footer warning */}
        <div style={{
          marginTop: "28px", padding: "14px 18px",
          background: t.warningBg, border: `1px solid ${t.warningBorder}`,
          borderRadius: "10px", display: "flex", alignItems: "center", gap: "10px",
        }}>
          <span style={{ fontSize: "14px" }}>⚠️</span>
          <span style={{ fontSize: "12px", color: t.warningText }}>
            Changes take effect immediately. Deactivated users lose access at next login attempt.
          </span>
        </div>
      </div>

      {/* Confirm dialog */}
      {confirm && (
        <ConfirmDialog
          user={confirm.user}
          action={confirm.action}
          isPending={toggleMutation.isPending}
          onConfirm={() => toggleMutation.mutate(confirm.user.user_id)}
          onCancel={() => setConfirm(null)}
        />
      )}

      {/* Toast */}
      {toast && (
        <div style={{
          position: "fixed", bottom: "28px", left: "50%",
          transform: "translateX(-50%)", zIndex: 200,
          background: t.toastBg, border: `1px solid ${t.toastBorder}`,
          borderRadius: "10px", padding: "12px 20px",
          color: t.toastText, fontSize: "13px",
          boxShadow: "0 8px 32px rgba(0,0,0,.5)",
          animation: "toastIn .25s ease", whiteSpace: "nowrap",
        }}>
          ✓ {toast}
        </div>
      )}

      <style>{`
        @import url('https://fonts.googleapis.com/css2?family=DM+Mono:wght@400;500&family=DM+Sans:wght@400;500;600;700&display=swap');
        @keyframes rowIn   { from{opacity:0;transform:translateY(6px)} to{opacity:1;transform:translateY(0)} }
        @keyframes toastIn { from{opacity:0;transform:translateX(-50%) translateY(10px)} to{opacity:1;transform:translateX(-50%) translateY(0)} }
        @keyframes pulse   { 0%,100%{opacity:.4} 50%{opacity:.7} }
        @keyframes spin    { to{transform:rotate(360deg)} }
        @media(max-width:780px){
          div[style*="gridTemplateColumns:2fr 2fr"]{grid-template-columns:1fr !important;}
        }
      `}</style>
    </div>
  );
}

// ── Owner Login Page — mount this at /accounts/login ─────────────────────
export function AccountsLoginPage() {
  const navigate = useNavigate();

  // Already have a verified header stored? Go straight to dashboard —
  // usersQuery will still 401-and-redirect if it's since gone stale.
  useEffect(() => {
    if (sessionStorage.getItem("owner_basic_auth")) {
      navigate("/accounts", { replace: true });
    }
  }, [navigate]);

  function onLogin() {
    navigate("/accounts", { replace: true });
  }

  return <LoginScreen onLogin={onLogin} />;
}

// ── Accounts Dashboard Page — mount this at /accounts ────────────────────
export default function AccountsPage() {
  const navigate = useNavigate();

  // Not authed? Redirect to login
  useEffect(() => {
    if (!sessionStorage.getItem("owner_basic_auth")) {
      navigate("/accounts/login", { replace: true });
    }
  }, [navigate]);

  function logout() {
    sessionStorage.removeItem("owner_basic_auth");
    sessionStorage.removeItem("owner_username");
    navigate("/accounts/login", { replace: true });
  }

  // Don't render dashboard until confirmed authed
  if (!sessionStorage.getItem("owner_basic_auth")) return null;

  return <AccountsDashboard onLogout={logout} />;
}