// frontend/src/pages/DownloadPage.tsx
import { useContext, useEffect, useState } from "react"
import { Link } from "react-router-dom"
import { ThemeContext } from "../App"
import { BRAND_NAME } from "../lib/brand"

// GitHub repo ("owner/repo") that holds your published desktop installers.
// It must be PUBLIC so visitors' browsers can read it.
const RELEASES_REPO = "Manish-Awase/checkmybond-releases"

type Asset = { name: string; browser_download_url: string; size: number }

function formatSize(bytes: number) {
  if (bytes <= 0) return ""
  return bytes >= 1073741824 ? `${(bytes / 1073741824).toFixed(1)} GB` : `${Math.round(bytes / 1048576)} MB`
}

export default function DownloadPage() {
  const { dark } = useContext(ThemeContext)
  const [status, setStatus] = useState<"loading" | "ok" | "error">("loading")
  const [tag, setTag] = useState("")
  const [assets, setAssets] = useState<Asset[]>([])

  // Read the latest published (non-draft) release, so links never go stale when you ship a new version.
  useEffect(() => {
    const ctrl = new AbortController()
    fetch(`https://api.github.com/repos/${RELEASES_REPO}/releases/latest`, {
      signal: ctrl.signal,
      headers: { Accept: "application/vnd.github+json" },
    })
      .then((r) => { if (!r.ok) throw new Error(String(r.status)); return r.json() })
      .then((d) => { setTag(d.tag_name || ""); setAssets(d.assets || []); setStatus("ok") })
      .catch((e) => { if (e.name !== "AbortError") setStatus("error") })
    return () => ctrl.abort()
  }, [])

  const t = {
    bg:         dark ? "#060912" : "#f8fafc",
    text:       dark ? "#e2e8f0" : "#0f172a",
    subtext:    dark ? "#94a3b8" : "#475569",
    card:       dark ? "#0f172a" : "#ffffff",
    cardBorder: dark ? "#1e293b" : "#e2e8f0",
    accent:     "#3b82f6",
    accentSoft: dark ? "rgba(59,130,246,.12)" : "rgba(59,130,246,.08)",
  }

  const platforms = [
    {
      name: "Windows",
      arch: "x64",
      variants: [
        { label: "Download .exe", match: /\.exe$/i },
        { label: "Download .msi", match: /\.msi$/i },
      ],
      icon: (
        <svg width="28" height="28" viewBox="0 0 24 24" fill="currentColor">
          <path d="M0 3.449L9.75 2.1v9.451H0m10.949-9.602L24 0v11.4H10.949M0 12.6h9.75v9.451L0 20.699M10.949 12.6H24V24l-12.9-1.801"/>
        </svg>
      ),
      note: "Windows 10 & 11",
    },
    {
      name: "macOS",
      arch: "Apple Silicon / Intel",
      variants: [
        { label: "Apple Silicon .dmg", match: /(aarch64|arm64).*\.dmg$/i },
        { label: "Intel .dmg", match: /(x64|x86_64|amd64|intel).*\.dmg$/i },
      ],
      icon: (
        <svg width="28" height="28" viewBox="0 0 24 24" fill="currentColor">
          <path d="M18.71 19.5c-.83 1.24-1.71 2.45-3.05 2.47-1.34.03-1.77-.79-3.29-.79-1.53 0-2 .77-3.27.82-1.31.05-2.3-1.32-3.14-2.53C4.25 17 2.94 12.45 4.7 9.39c.87-1.52 2.43-2.48 4.12-2.51 1.28-.02 2.5.87 3.29.87.78 0 2.26-1.07 3.8-.91.65.03 2.47.26 3.64 1.98-.09.06-2.17 1.28-2.15 3.81.03 3.02 2.65 4.03 2.68 4.04-.03.07-.42 1.44-1.38 2.83M13 3.5c.73-.83 1.21-1.98 1.07-3.11-1.05.05-2.31.7-3.06 1.55-.67.76-1.26 1.97-1.1 3.09 1.18.09 2.38-.68 3.09-1.53"/>
        </svg>
      ),
      note: "macOS 12+",
    },
    {
      name: "Linux",
      arch: "x64 / ARM64",
      variants: [
        { label: "x64 .AppImage", match: /(x86_64|x64|amd64).*\.AppImage$/i },
        { label: "ARM64 .AppImage", match: /(aarch64|arm64).*\.AppImage$/i },
        { label: "x64 .deb", match: /(x86_64|x64|amd64).*\.deb$/i },
      ],
      icon: (
        <svg width="28" height="28" viewBox="0 0 24 24" fill="currentColor">
          <path d="M12.504 0c-.155 0-.315.008-.48.021-4.226.333-3.105 4.807-3.17 6.298-.076 1.092-.3 1.953-1.05 3.02-.885 1.051-2.127 2.75-2.716 4.521-.278.832-.41 1.684-.287 2.489a.424.424 0 00-.11.135c-.26.268-.45.6-.663.839-.199.199-.485.267-.797.4-.313.136-.658.269-.864.68-.09.189-.136.394-.132.602 0 .199.027.4.055.536.058.399.116.728.04.97-.249.68-.28 1.145-.106 1.484.174.334.535.47.94.601.81.2 1.91.135 2.774.6.926.466 1.866.701 2.916.555.525-.076 1.04-.24 1.536-.408.497-.168.975-.34 1.486-.34.51 0 .99.172 1.486.34.496.168 1.01.332 1.536.408 1.05.146 1.99-.09 2.916-.555.864-.465 1.964-.4 2.774-.6.405-.131.766-.267.94-.601.174-.339.143-.804-.106-1.484-.076-.242-.018-.571.04-.97.028-.136.055-.337.055-.536.004-.208-.042-.413-.132-.602-.206-.411-.551-.544-.864-.68-.312-.133-.598-.201-.797-.4-.213-.239-.403-.571-.663-.839a.424.424 0 00-.11-.135c.123-.805-.009-1.657-.287-2.489-.589-1.771-1.831-3.47-2.716-4.521-.75-1.067-.974-1.928-1.05-3.02-.065-1.491 1.056-5.965-3.17-6.298C12.819.008 12.659 0 12.504 0z"/>
        </svg>
      ),
      note: "Ubuntu 20.04+, Fedora",
    },
  ]

  return (
    <div style={{
      minHeight: "100vh", background: t.bg, color: t.text,
      fontFamily: "'Sora', sans-serif", padding: "80px 24px",
    }}>
      <div style={{ maxWidth: 720, margin: "0 auto" }}>
        <Link to="/home" style={{
          color: t.subtext, textDecoration: "none", fontSize: 13,
          fontWeight: 600, display: "inline-flex", alignItems: "center", gap: 6,
          marginBottom: 32,
        }}>
          ← Back to Home
        </Link>

        <h1 style={{ fontSize: 32, fontWeight: 700, margin: "0 0 8px" }}>
          Download {BRAND_NAME}
        </h1>
        <p style={{ fontSize: 16, color: t.subtext, margin: "0 0 36px" }}>
          Get the native desktop app for the best experience. Works offline, with native file pickers and faster uploads.
        </p>
        {status === "ok" && tag && (
          <p style={{ fontSize: 13, color: t.subtext, margin: "-24px 0 28px" }}>
            Latest version: <b>{tag}</b>
          </p>
        )}

        <div style={{ display: "flex", flexDirection: "column", gap: 16 }}>
          {platforms.map((p) => (
            <div key={p.name} style={{
              background: t.card, border: `1px solid ${t.cardBorder}`,
              borderRadius: 14, padding: "24px 28px",
              display: "flex", alignItems: "center", gap: 20,
              flexWrap: "wrap",
            }}>
              <div style={{
                width: 52, height: 52, borderRadius: 12,
                background: t.accentSoft, color: t.accent,
                display: "flex", alignItems: "center", justifyContent: "center",
                flexShrink: 0,
              }}>
                {p.icon}
              </div>

              <div style={{ flex: 1, minWidth: 160 }}>
                <div style={{ fontSize: 17, fontWeight: 700, marginBottom: 2 }}>
                  {p.name}
                </div>
                <div style={{ fontSize: 13, color: t.subtext }}>
                  {p.arch} · {p.note}
                </div>
              </div>

              <div style={{ display: "flex", flexDirection: "column", gap: 8, flexShrink: 0 }}>
                {(() => {
                  const found = p.variants
                    .map((v) => ({ ...v, asset: assets.find((a) => v.match.test(a.name)) }))
                    .filter((v) => v.asset)
                  if (status === "loading") {
                    return <span style={{ fontSize: 13, color: t.subtext }}>Checking latest version…</span>
                  }
                  if (found.length === 0) {
                    return (
                      <span style={{ fontSize: 13, color: t.subtext, fontWeight: 600 }}>
                        {status === "error" ? "Downloads are temporarily unavailable. Please try again later." : "Not available yet"}
                      </span>
                    )
                  }
                  return found.map((v) => (
                    <a key={v.label} href={v.asset!.browser_download_url}
                      style={{
                        background: "linear-gradient(135deg, #2563eb, #1d4ed8)",
                        color: "white", textDecoration: "none",
                        padding: "10px 20px", borderRadius: 10,
                        fontWeight: 700, fontSize: 13, whiteSpace: "nowrap",
                        textAlign: "center",
                      }}
                    >
                      {v.label}{formatSize(v.asset!.size) ? ` · ${formatSize(v.asset!.size)}` : ""}
                    </a>
                  ))
                })()}
              </div>
            </div>
          ))}
        </div>

        {/* Web fallback */}
        <div style={{
          marginTop: 32, padding: "24px 28px",
          background: t.accentSoft,
          border: `1px solid ${dark ? "rgba(59,130,246,.2)" : "rgba(59,130,246,.15)"}`,
          borderRadius: 14, textAlign: "center",
        }}>
          <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 6 }}>
            Prefer using a browser?
          </div>
          <p style={{ fontSize: 14, color: t.subtext, margin: "0 0 16px" }}>
            The web app has the same features and works on any device.
          </p>
          <Link to="/login" style={{
            color: t.accent, textDecoration: "none", fontWeight: 700, fontSize: 14,
          }}>
            Open Web App →
          </Link>
        </div>
      </div>
    </div>
  )
}
