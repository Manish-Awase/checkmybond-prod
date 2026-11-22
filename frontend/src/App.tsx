// frontend/src/App.tsx
import { createContext, useContext, useState } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { BrowserRouter, Routes, Route, Navigate, NavLink, useNavigate } from "react-router-dom"
import { AuthProvider, useAuth } from "./context/AuthContext"

// ── Existing pages (always active) ───────────────────────────────────────────
import LoginPage     from "./pages/LoginPage"
import UploadPage    from "./pages/UploadPage"
import ProfilePage   from "./pages/ProfilePage"
import ReviewPage    from "./pages/ReviewPage"
import DashboardPage from "./pages/DashboardPage"


import RegisterPage              from "./pages/RegisterPage"
import QueuePage                 from "./pages/QueuePage"
import AdminPage                 from "./pages/AdminPage"
import AccountsPage, { AccountsLoginPage } from "./pages/AccountsPage"
import RegisterPageInAdminPage from "./pages/RegisterPageInAdminPage"

// Plan selection / payment / Google OAuth landing
import SelectPlanPage            from "./pages/SelectPlanPage"
import PaymentCalculationPage    from "./pages/PaymentCalculationPage"
import OAuthCallbackPage         from "./pages/OAuthCallbackPage"

// Debug page
import PipelineMonitorPage from "./pages/PipelineMonitorPage"
import SystemHealthPage from "./pages/SystemHealthPage"
import PerformancePage from "./pages/PerformancePage"

// ── New public marketing pages ───────────────────────────────────────────────
import LandingPage from "./pages/LandingPage"
import UsageGuidePage from "./pages/UsageGuidePage"
import ContactPage from "./pages/ContactPage"
import DownloadPage from "./pages/DownloadPage"
import PrivacyPolicyPage from "./pages/PrivacyPolicyPage"
import TermsOfServicePage from "./pages/TermsOfServicePage"

// ── Password reset pages ─────────────────────────────────────────────────────
import ForgotPasswordPage from "./pages/ForgotPasswordPage"
import ResetPasswordPage  from "./pages/ResetPasswordPage"

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 2, staleTime: 30_000 } },
})

// ── Theme context ─────────────────────────────────────────────────────────────
export const ThemeContext = createContext<{
  dark: boolean
  toggleTheme: () => void
}>({ dark: false, toggleTheme: () => {} })

// ── Protected route ───────────────────────────────────────────────────────────
// This component checks if the user is authenticated before rendering the children. If not authenticated, it redirects to the login page.
function Protected({ children }: { children: React.ReactNode }) {
  const { isAuthed } = useAuth()
  return isAuthed ? <>{children}</> : <Navigate to="/login" replace />
}
// improved protected function that checks for user roles when accessing certain routes
function ProtectedRole({ children, roles }: { children: React.ReactNode; roles: string[] }) {
  const { isAuthed, user } = useAuth()
  if (!isAuthed)                    return <Navigate to="/login"  replace />
  if (!roles.includes(user?.role!)) return <Navigate to="/login"       replace />
  return <>{children}</>
}

function ProtectedDebug({ children }: { children: React.ReactNode }) {
  const { isAuthed, user } = useAuth()
  if (!isAuthed) return <Navigate to="/login" replace />
  if (user?.role !== "debug" && user?.role !== "admin") return <Navigate to="/login" replace />
  return <>{children}</>
}

// ── Owner portal guard (defense-in-depth) ───────────────────────────────────
function ProtectedOwner({ children }: { children: React.ReactNode }) {
  const hasOwnerAuth = !!sessionStorage.getItem("owner_basic_auth")
  if (!hasOwnerAuth) return <Navigate to="/accounts/login" replace />
  return <>{children}</>
}

// ── NavBar ────────────────────────────────────────────────────────────────────
function NavBar() {
  const { user, logout }      = useAuth()
  const { dark, toggleTheme } = useContext(ThemeContext)
  const navigate              = useNavigate()
  const [dropOpen, setDrop]   = useState(false)

  if (!user) return null

  const ROLE_STYLE: Record<string, { color: string; bg: string; border: string }> = {
  analyst:  { color: "#3b82f6", bg: "rgba(59,130,246,.1)",  border: "rgba(59,130,246,.3)"  },
  reviewer: { color: "#7c3aed", bg: "rgba(124,58,237,.1)",  border: "rgba(124,58,237,.3)"  },
  admin:    { color: "#d97706", bg: "rgba(217,119,6,.1)",   border: "rgba(217,119,6,.3)"   },
  debug:    { color: "#22c55e", bg: "rgba(34,197,94,.1)",   border: "rgba(34,197,94,.3)"   }, // ← add
}
  const rs = ROLE_STYLE[user.role] ?? ROLE_STYLE.analyst

  return (
    <nav className="nav">
      <div className="nav-brand" onClick={() => navigate(
        user.role === "reviewer" ? "/queue" :
        user.role === "admin"    ? "/dashboard" : "/upload"
      )} style={{ cursor: "pointer" }}>
        <span className="nav-logo">⬡</span>
        <span className="nav-name">BondQi</span>
      </div>

      <div className="nav-links">
        {(user.role === "analyst" || user.role === "admin") && (
          <NavLink to="/upload" className={({ isActive }) => `nav-link ${isActive ? "active" : ""}`}>
            Upload
          </NavLink>
        )}
        <NavLink to="/profile" className={({ isActive }) => `nav-link ${isActive ? "active" : ""}`}>
          Profile
        </NavLink>

        {/* Uncomment when QueuePage exists:*/}
        {(user.role === "reviewer" || user.role === "admin") && (
          <NavLink to="/queue" className={({ isActive }) => `nav-link ${isActive ? "active" : ""}`}>
            Queue
          </NavLink>
        )}


        {(user.role === "reviewer" || user.role === "admin") && (
          <NavLink to="/dashboard" className={({ isActive }) => `nav-link ${isActive ? "active" : ""}`}>
            Dashboard
          </NavLink>
        )}

        {/* Uncomment when AdminPage exists: */}
        {(user.role === "admin")&& (
          <NavLink to="/admin" className={({ isActive }) => `nav-link ${isActive ? "active" : ""}`}>
            Admin
          </NavLink>
        )}

        {(user.role === "admin")&& (
          <NavLink to="/registeremployee" className={({ isActive }) => `nav-link ${isActive ? "active" : ""}`}>
            RegisterEmployee
          </NavLink>
        )}
        {(user.role === "debug"|| user.role === "admin") && (
          <NavLink to="/pipeline1" className={({ isActive }) => `nav-link ${isActive ? "active" : ""}`}>
            Pipeline
          </NavLink>
        )}

        {(user.role === "debug"|| user.role === "admin") && (
          <NavLink to="/pipeline2" className={({ isActive }) => `nav-link ${isActive ? "active" : ""}`}>
            Performance
          </NavLink>
        )}

        {(user.role === "analyst" || user.role === "admin") && (
          <NavLink to="/select-plan" className={({ isActive }) => `nav-link ${isActive ? "active" : ""}`}>
            Plans
          </NavLink>
        )}

      </div>

      {/* User dropdown */}
      <div
        className="nav-right"
        onMouseEnter={() => setDrop(true)}
        onMouseLeave={() => setDrop(false)}
        style={{ marginLeft: "auto", position: "relative" }}
      >
        <div className="nav-user-trigger">
          <span className="nav-role" style={{ color: rs.color, background: rs.bg, borderColor: rs.border }}>
            {user.role}
          </span>
          {dropOpen && (
            <div className="nav-dropdown">
              <div className="nav-dropdown-user">
                <div className="nav-dropdown-username">{user.username}</div>
                <div className="nav-dropdown-email">{user.email}</div>
                <span className="nav-dropdown-role" style={{ color: rs.color, background: rs.bg, borderColor: rs.border }}>
                  {user.role}
                </span>
              </div>
              <div className="nav-dropdown-divider" />
              <div className="nav-dropdown-item" onClick={toggleTheme}>
                <span>{dark ? "☀️" : "🌙"}</span>
                <span>{dark ? "Light theme" : "Dark theme"}</span>
                <span className="nav-dropdown-toggle">
                  <span className={`toggle-track ${dark ? "on" : ""}`}>
                    <span className="toggle-thumb" />
                  </span>
                </span>
              </div>
              <div className="nav-dropdown-divider" />
              <button
                className="nav-dropdown-logout"
                onClick={() => { logout(); navigate("/login") }}
              >
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M9 21H5a2 2 0 01-2-2V5a2 2 0 012-2h4M16 17l5-5-5-5M21 12H9"/>
                </svg>
                Sign out
              </button>
            </div>
          )}
        </div>
      </div>

      <style>{`
        @import url('https://fonts.googleapis.com/css2?family=Sora:wght@400;600;700&display=swap');
        .nav {
          display: flex; align-items: center; height: 54px;
          padding: 0 24px; gap: 20px;
          background: ${dark ? "#0f172a" : "#ffffff"};
          border-bottom: 1px solid ${dark ? "#1e293b" : "#e2e8f0"};
          position: sticky; top: 0; z-index: 100;
          font-family: 'Sora', sans-serif;
          box-shadow: 0 1px 3px rgba(0,0,0,0.08);
        }
        .nav-brand { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
        .nav-logo  { font-size: 20px; color: #3b82f6; filter: drop-shadow(0 0 6px rgba(59,130,246,.5)); }
        .nav-name  { font-size: 15px; font-weight: 700; color: ${dark ? "#e2e8f0" : "#1e293b"}; letter-spacing: -.01em; }
        .nav-links { display: flex; gap: 2px; }
        .nav-link  {
          font-size: 13px; font-weight: 600; color: ${dark ? "#64748b" : "#64748b"};
          padding: 6px 14px; border-radius: 6px;
          text-decoration: none; transition: all .15s;
        }
        .nav-link:hover  { color: ${dark ? "#e2e8f0" : "#1e293b"}; background: ${dark ? "#1e293b" : "#f1f5f9"}; }
        .nav-link.active { color: #3b82f6; background: rgba(59,130,246,.08); }
        .nav-right { margin-left: auto; }
        .nav-user-trigger { position: relative; cursor: pointer; }
        .nav-role {
          font-size: 10px; font-weight: 700; text-transform: uppercase;
          letter-spacing: .07em; border: 1px solid; border-radius: 20px;
          padding: 4px 10px; cursor: pointer; display: inline-block;
        }
        .nav-dropdown {
          position: absolute; right: 0; top: 100%;
          background: ${dark ? "#1e293b" : "#ffffff"};
          border: 1px solid ${dark ? "#334155" : "#e2e8f0"};
          border-radius: 12px; min-width: 220px;
          box-shadow: 0 8px 24px rgba(0,0,0,0.12);
          z-index: 200; overflow: hidden;
          animation: ddFadeIn .15s ease;
        }
        @keyframes ddFadeIn { from{opacity:0;transform:translateY(-4px)} to{opacity:1;transform:translateY(0)} }
        .nav-dropdown-user { padding: 14px 16px; display: flex; flex-direction: column; gap: 4px; }
        .nav-dropdown-username { font-size: 14px; font-weight: 700; color: ${dark ? "#e2e8f0" : "#1e293b"}; }
        .nav-dropdown-email    { font-size: 12px; color: #94a3b8; }
        .nav-dropdown-role {
          font-size: 10px; font-weight: 700; text-transform: uppercase;
          letter-spacing: .07em; border: 1px solid; border-radius: 20px;
          padding: 2px 8px; display: inline-block; margin-top: 4px;
        }
        .nav-dropdown-divider  { height: 1px; background: ${dark ? "#334155" : "#f1f5f9"}; }
        .nav-dropdown-item {
          display: flex; align-items: center; gap: 10px;
          padding: 12px 16px; cursor: pointer;
          font-size: 13px; font-weight: 600;
          color: ${dark ? "#94a3b8" : "#64748b"}; transition: all .15s;
        }
        .nav-dropdown-item:hover { background: ${dark ? "#0f172a" : "#f8fafc"}; color: ${dark ? "#e2e8f0" : "#1e293b"}; }
        .nav-dropdown-toggle { margin-left: auto; }
        .toggle-track {
          display: inline-flex; align-items: center;
          width: 36px; height: 20px; border-radius: 20px;
          background: #cbd5e1; padding: 2px; transition: background .2s;
        }
        .toggle-track.on { background: #3b82f6; }
        .toggle-thumb {
          width: 16px; height: 16px; border-radius: 50%;
          background: white; transition: transform .2s; transform: translateX(0);
        }
        .toggle-track.on .toggle-thumb { transform: translateX(16px); }
        .nav-dropdown-logout {
          display: flex; align-items: center; gap: 8px;
          width: 100%; padding: 12px 16px;
          background: none; border: none; cursor: pointer;
          font-family: 'Sora', sans-serif; font-size: 13px;
          font-weight: 600; color: ${dark ? "#94a3b8" : "#64748b"};
          transition: all .15s; text-align: left;
        }
        .nav-dropdown-logout:hover { background: rgba(239,68,68,.06); color: #ef4444; }
      `}</style>
    </nav>
  )
}

// ── Layout ────────────────────────────────────────────────────────────────────
function Layout({ children }: { children: React.ReactNode }) {
  const { dark } = useContext(ThemeContext)
  return (
    <div style={{
      display: "flex", flexDirection: "column", minHeight: "100vh",
      background: dark ? "#060912" : "#f1f5f9",
      color: dark ? "#e2e8f0" : "#1e293b",
      transition: "background .2s, color .2s",
    }}>
      <NavBar />
      <main style={{ flex: 1, overflow: "auto" }}>{children}</main>
    </div>
  )
}

// ── Default redirect ──────────────────────────────────────────────────────────
function DefaultRedirect() {
  const { isAuthed, user } = useAuth()
  if (!isAuthed)                 return <Navigate to="/home"     replace />
  if (user?.role === "reviewer") return <Navigate to="/queue"     replace /> 
  if (user?.role === "admin")    return <Navigate to="/dashboard" replace />
  if (user?.role === "debug")    return <Navigate to="/pipeline1" replace />
  return                                <Navigate to="/upload"    replace />
}

// ── App ───────────────────────────────────────────────────────────────────────
export default function App() {
  const [dark, setDark] = useState(() => localStorage.getItem("theme") === "dark")

  const toggleTheme = () => {
    setDark(prev => {
      const next = !prev
      localStorage.setItem("theme", next ? "dark" : "light")
      return next
    })
  }

  return (
    <ThemeContext.Provider value={{ dark, toggleTheme }}>
      <QueryClientProvider client={queryClient}>
        <AuthProvider>
          <BrowserRouter>
            <Routes>

              {/* ── Public marketing pages ────────────────────────────────── */}
              <Route path="/home"      element={<LandingPage />} />
              <Route path="/guide"     element={<UsageGuidePage />} />
              <Route path="/contact"   element={<ContactPage />} />
              <Route path="/downloads" element={<DownloadPage />} />
              <Route path="/privacy"   element={<PrivacyPolicyPage />} />
              <Route path="/terms"     element={<TermsOfServicePage />} />

              {/* ── Password reset (public) ───────────────────────────────── */}
              <Route path="/forgot-password" element={<ForgotPasswordPage />} />
              <Route path="/reset-password"  element={<ResetPasswordPage />} />

              {/* ── Owner portal ────────────────────────────────────────────*/}
              <Route path="/accounts/login" element={<AccountsLoginPage />} />
              <Route path="/accounts"       element={
                <ProtectedOwner><AccountsPage /></ProtectedOwner>
              } />
              {/* Owner-only, self-gated via HTTP Basic auth (not JWT) —
                  access manually at /system-health, not in nav bar. */}
              <Route path="/system-health"  element={
                <ProtectedOwner><SystemHealthPage /></ProtectedOwner>
              } />


              {/* ── Public routes ── */}
              <Route path="/login"    element={<LoginPage />} />

              {/* Uncomment when RegisterPage exists: */}
              <Route path="/register" element={<RegisterPage />} />

              {/* Google redirects here after auth; not logged in yet at this
                  point (OAuthCallbackPage sets the session itself), so this
                  stays public like /login and /register. */}
              <Route path="/oauth-callback" element={<OAuthCallbackPage />} />

              {/* ── Protected routes ── */}

              <Route path="/registeremployee" element={
                <ProtectedRole roles={["admin"]}>
                  <Layout><RegisterPageInAdminPage /></Layout>
                </ProtectedRole>
              } />

              <Route path="/upload" element={
                <ProtectedRole roles={["analyst", "admin"]}>
                  <Layout><UploadPage /></Layout>
                </ProtectedRole>
              } />

              <Route path="/profile" element={
                <Protected>
                  <Layout><ProfilePage /></Layout>
                </Protected>
              } />

              <Route path="/review/:id" element={
                <ProtectedRole roles={["analyst", "reviewer", "admin"]}>
                  <Layout><ReviewPage /></Layout>
                </ProtectedRole>
              } />


             <Route path="/dashboard" element={
                <ProtectedRole roles={["reviewer", "admin"]}>
                  <Layout><DashboardPage /></Layout>
                </ProtectedRole>
              } />

              <Route path="/queue" element={
                <ProtectedRole roles={["analyst", "reviewer", "admin"]}>
                  <Layout><QueuePage /></Layout>
                </ProtectedRole>
              } />

              <Route path="/admin" element={
                <ProtectedRole roles={["admin"]}>
                  <Layout><AdminPage /></Layout>
                </ProtectedRole>
              } />

              {/* ── Plan selection + payment ── */}
              <Route path="/select-plan" element={
                <ProtectedRole roles={["analyst", "admin"]}>
                  <Layout><SelectPlanPage /></Layout>
                </ProtectedRole>
              } />

              <Route path="/payment/:planId" element={
                <ProtectedRole roles={["analyst", "admin"]}>
                  <Layout><PaymentCalculationPage /></Layout>
                </ProtectedRole>
              } />

              {/* ── Pipeline monitor / Performance ───────── */}
              <Route path="/pipeline1" element={
                <ProtectedDebug><Layout><PipelineMonitorPage /></Layout></ProtectedDebug>
              } />
              <Route path="/pipeline2" element={
                <ProtectedDebug><Layout><PerformancePage /></Layout></ProtectedDebug>
              } />
              {/* ── Fallback ── */}
              <Route path="*" element={<DefaultRedirect />} />
            </Routes>
          </BrowserRouter>
        </AuthProvider>
      </QueryClientProvider>
    </ThemeContext.Provider>
  )
}
