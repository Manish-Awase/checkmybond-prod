// frontend/src/components/BackendGate.tsx
//
// Replaces the earlier splash.html -> navigate() -> index.html design, which
// hit repeated Windows/WebView2-specific issues (wrong URL scheme, wrong
// enum type, and finally an unresolved "refused to connect" on navigate()
// between two different asset origins). This is simpler and more robust:
// the window always loads index.html once, and this component itself shows
// a loading state (identical look to the old splash screen) until the local
// gateway sidecar answers /health, using a plain fetch() — ordinary app
// code, not a Tauri-specific navigation mechanism.
//
// In browser dev mode (npm run dev, no Tauri wrapper), this renders children
// immediately — there's no sidecar to wait for.
import { useEffect, useState } from "react"

const isTauri =
  typeof window !== "undefined" &&
  ("__TAURI_INTERNALS__" in window || "__TAURI__" in window)

const BASE_URL = import.meta.env.VITE_API_BASE_URL ?? ""
const HEALTH_URL = `${BASE_URL}/health`
const MAX_ATTEMPTS = 20 // 20 * 500ms = 10 seconds, matches the old Rust timeout

type Status = "checking" | "ready" | "failed"

export default function BackendGate({ children }: { children: React.ReactNode }) {
  const [status, setStatus] = useState<Status>(isTauri ? "checking" : "ready")
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    if (!isTauri) return
    let cancelled = false

    const check = async (n: number) => {
      try {
        const res = await fetch(HEALTH_URL, { signal: AbortSignal.timeout(2000) })
        if (res.ok && !cancelled) {
          setStatus("ready")
          return
        }
      } catch {
        // connection refused / not ready yet — fall through to retry
      }
      if (cancelled) return
      if (n >= MAX_ATTEMPTS) {
        setStatus("failed")
      } else {
        setAttempt(n + 1)
        setTimeout(() => check(n + 1), 500)
      }
    }

    check(0)
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  if (status === "ready") return <>{children}</>

  const dark = "#0f172a"

  if (status === "checking") {
    return (
      <div style={styles.wrap(dark)}>
        <div style={styles.spinner} />
        <h1 style={styles.h1}>Starting up...</h1>
        <p style={styles.p}>Loading backend services. This may take a few seconds.</p>
        <style>{`@keyframes bg-spin { to { transform: rotate(360deg); } }`}</style>
      </div>
    )
  }

  // status === "failed"
  return (
    <div style={styles.wrap(dark)}>
      <div style={styles.errorIcon}>!</div>
      <h1 style={styles.h1}>Failed to start</h1>
      <p style={styles.p}>
        The backend services did not become ready in time. Check that nothing
        else is using port 9000, then retry.
      </p>
      <button
        style={styles.button}
        onClick={() => {
          setAttempt(0)
          setStatus("checking")
          // re-trigger the effect path by reloading — simplest reliable retry
          window.location.reload()
        }}
      >
        Retry
      </button>
    </div>
  )
}

const styles = {
  wrap: (bg: string): React.CSSProperties => ({
    background: bg,
    color: "#e2e8f0",
    fontFamily:
      '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    height: "100vh",
    textAlign: "center",
    padding: 24,
  }),
  spinner: {
    width: 48,
    height: 48,
    border: "4px solid rgba(255,255,255,0.1)",
    borderTopColor: "#38bdf8",
    borderRadius: "50%",
    animation: "bg-spin 1s linear infinite",
    marginBottom: 24,
  } as React.CSSProperties,
  errorIcon: {
    width: 56,
    height: 56,
    background: "#ef4444",
    borderRadius: "50%",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    fontSize: 28,
    marginBottom: 20,
  } as React.CSSProperties,
  h1: { fontSize: "1.25rem", fontWeight: 500 } as React.CSSProperties,
  p: {
    marginTop: 8,
    color: "#94a3b8",
    fontSize: "0.875rem",
    maxWidth: 400,
    lineHeight: 1.5,
  } as React.CSSProperties,
  button: {
    marginTop: 28,
    background: "#38bdf8",
    color: "#0f172a",
    border: "1px solid #38bdf8",
    padding: "10px 20px",
    borderRadius: 6,
    fontSize: "0.875rem",
    fontWeight: 500,
    cursor: "pointer",
  } as React.CSSProperties,
}
