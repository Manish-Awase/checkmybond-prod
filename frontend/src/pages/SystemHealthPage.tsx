// frontend/src/pages/SystemHealthPage.tsx
// Internal ops page — access at /system-health (not in nav bar), same
// pattern as PipelineMonitorPage. Shows whether Postgres/Redis/Qdrant are
// reachable and recent error/payment-failure rates from audit_log and
// plan_transactions.
//
// Deliberately does NOT reuse lib/api.ts's apiFetch: apiFetch clears the
// logged-in user's session and redirects to /login on any 401, which would
// mean mistyping the owner password here would also log out whichever
// admin is using this page in another tab. This page uses ownerHealthCheck
// (raw fetch, no auto-logout) instead.
import { useState, useEffect, useCallback } from "react";
import { ownerHealthCheck, HealthResponse } from "../lib/api";

const STATUS_COLORS: Record<string, string> = {
  healthy: "#4ade80",
  degraded: "#fbbf24",
  unhealthy: "#f87171",
};

const POLL_INTERVAL_MS = 15000;

export default function SystemHealthPage() {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [authed, setAuthed] = useState(false);
  const [authError, setAuthError] = useState("");
  const [data, setData] = useState<HealthResponse | null>(null);
  const [fetchError, setFetchError] = useState("");
  const [loading, setLoading] = useState(false);

  const fetchHealth = useCallback(async (user: string, pass: string) => {
    setLoading(true);
    setFetchError("");
    try {
      const health = await ownerHealthCheck(btoa(`${user}:${pass}`));
      setData(health);
      setAuthed(true);
    } catch (err: any) {
      if (err.status === 401) {
        setAuthed(false);
        setAuthError("Invalid owner credentials");
        return;
      }
      setFetchError(err.message || "Could not reach the server");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!authed) return;
    const interval = setInterval(() => fetchHealth(username, password), POLL_INTERVAL_MS);
    return () => clearInterval(interval);
  }, [authed, username, password, fetchHealth]);

  const handleLogin = (e: React.FormEvent) => {
    e.preventDefault();
    setAuthError("");
    fetchHealth(username, password);
  };

  if (!authed) {
    return (
      <div style={{ display: "flex", justifyContent: "center", alignItems: "center", minHeight: "100vh", background: "#0b0e14" }}>
        <form onSubmit={handleLogin} style={{
          background: "#141926", border: "1px solid #1f2937", borderRadius: "12px",
          padding: "32px", width: "320px", display: "flex", flexDirection: "column", gap: "14px",
        }}>
          <h2 style={{ color: "#e5e7eb", fontFamily: "'Sora', sans-serif", fontSize: "16px", margin: 0 }}>
            System Health — Owner Login
          </h2>
          <input
            placeholder="Owner username" value={username} onChange={e => setUsername(e.target.value)}
            style={{ padding: "10px", borderRadius: "8px", border: "1px solid #374151", background: "#0b0e14", color: "#e5e7eb" }}
          />
          <input
            placeholder="Owner password" type="password" value={password} onChange={e => setPassword(e.target.value)}
            style={{ padding: "10px", borderRadius: "8px", border: "1px solid #374151", background: "#0b0e14", color: "#e5e7eb" }}
          />
          {authError && <div style={{ color: "#f87171", fontSize: "12px" }}>{authError}</div>}
          <button type="submit" disabled={loading} style={{
            padding: "10px", borderRadius: "8px", border: "none", background: "#635bff",
            color: "white", fontWeight: 700, cursor: loading ? "default" : "pointer",
          }}>
            {loading ? "Checking…" : "View health"}
          </button>
        </form>
      </div>
    );
  }

  return (
    <div style={{ minHeight: "100vh", background: "#0b0e14", padding: "32px", fontFamily: "'Sora', sans-serif" }}>
      <div style={{ display: "flex", alignItems: "center", gap: "12px", marginBottom: "24px" }}>
        <h1 style={{ color: "#e5e7eb", fontSize: "20px", margin: 0 }}>System Health</h1>
        {data && (
          <span style={{
            background: STATUS_COLORS[data.status] + "22", color: STATUS_COLORS[data.status],
            border: `1px solid ${STATUS_COLORS[data.status]}55`, borderRadius: "999px",
            padding: "3px 12px", fontSize: "12px", fontWeight: 700, textTransform: "uppercase",
          }}>
            {data.status}
          </span>
        )}
        <span style={{ color: "#6b7280", fontSize: "12px", marginLeft: "auto" }}>
          {data ? `Checked ${new Date(data.checkedAt).toLocaleTimeString()} — refreshes every 15s` : ""}
        </span>
      </div>

      {fetchError && <div style={{ color: "#f87171", marginBottom: "16px" }}>{fetchError}</div>}

      {data && (
        <>
          {/* Component pings — Postgres / Redis / Qdrant */}
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(180px, 1fr))", gap: "12px", marginBottom: "24px" }}>
            {data.components.map(c => (
              <div key={c.name} style={{
                background: "#141926", border: `1px solid ${c.healthy ? "#1f2937" : "#f8717155"}`,
                borderRadius: "10px", padding: "16px",
              }}>
                <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
                  <span style={{
                    width: "8px", height: "8px", borderRadius: "50%",
                    background: c.healthy ? "#4ade80" : "#f87171", display: "inline-block",
                  }} />
                  <span style={{ color: "#e5e7eb", fontSize: "13px", textTransform: "capitalize" }}>{c.name}</span>
                </div>
                {c.detail && <div style={{ color: "#f87171", fontSize: "11px", marginTop: "6px" }}>{c.detail}</div>}
              </div>
            ))}
          </div>

          {/* Request health — covers login/auth/data traffic generally, last 15 min */}
          {data.requests && (
            <div style={{ background: "#141926", border: "1px solid #1f2937", borderRadius: "10px", padding: "20px", marginBottom: "16px" }}>
              <h3 style={{ color: "#9ca3af", fontSize: "12px", textTransform: "uppercase", margin: "0 0 12px" }}>
                Request Traffic — last 15 min (login, auth, data endpoints)
              </h3>
              <div style={{ display: "flex", gap: "32px", flexWrap: "wrap" }}>
                <Stat label="Total requests" value={data.requests.totalRequests} />
                <Stat label="5xx errors" value={data.requests.errorCount} />
                <Stat label="Error rate" value={`${data.requests.errorRatePct.toFixed(2)}%`}
                      warn={data.requests.errorRatePct >= 2} />
                <Stat label="Avg latency" value={`${data.requests.avgDurationMs.toFixed(0)}ms`} />
              </div>
            </div>
          )}

          {/* Payment health — last 24h, separate from generic errors since a
              failed charge often comes back as a 200 with status:"failed" */}
          {data.payments && (
            <div style={{ background: "#141926", border: "1px solid #1f2937", borderRadius: "10px", padding: "20px" }}>
              <h3 style={{ color: "#9ca3af", fontSize: "12px", textTransform: "uppercase", margin: "0 0 12px" }}>
                Payments — last 24h
              </h3>
              <div style={{ display: "flex", gap: "32px", flexWrap: "wrap" }}>
                <Stat label="Total attempts" value={data.payments.totalAttempts} />
                <Stat label="Failed" value={data.payments.failedCount} />
                <Stat label="Failure rate" value={`${data.payments.failureRatePct.toFixed(2)}%`}
                      warn={data.payments.failureRatePct >= 15} />
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function Stat({ label, value, warn }: { label: string; value: string | number; warn?: boolean }) {
  return (
    <div>
      <div style={{ color: warn ? "#f87171" : "#e5e7eb", fontSize: "22px", fontWeight: 700 }}>{value}</div>
      <div style={{ color: "#6b7280", fontSize: "11px" }}>{label}</div>
    </div>
  );
}