// frontend/src/pages/DownloadPage.tsx
//
// Pulls real release assets from the public releases repo (see
// .github/workflows/release.yml's RELEASES_REPO and publish-to-releases-repo
// job) via GitHub's public REST API — no backend endpoint needed, this repo
// is public specifically so the Tauri updater and this page can both reach
// it without auth. Naming convention matches release.yml's
// "Collect bundle" step: BondQi_{version}_{arch}{ext}.
import { useEffect, useState, useContext } from "react"
import { ThemeContext } from "../App"

const RELEASES_REPO = "Manish-Awase/bondqi-releases"

interface ReleaseAsset {
  name: string
  browser_download_url: string
}
interface ReleaseInfo {
  tag_name: string
  published_at: string
  assets: ReleaseAsset[]
}

type Platform = "windows" | "macos-arm" | "macos-intel" | "linux" | "linux-arm"

const PLATFORM_LABELS: Record<Platform, string> = {
  "windows":     "Windows (x64)",
  "macos-arm":   "macOS (Apple Silicon)",
  "macos-intel": "macOS (Intel)",
  "linux":       "Linux (x64, .AppImage)",
  "linux-arm":   "Linux (ARM64, .AppImage)",
}

function matchAsset(assets: ReleaseAsset[], platform: Platform): ReleaseAsset | undefined {
  return assets.find(a => {
    const n = a.name.toLowerCase()
    if (n.endsWith(".sig")) return false
    switch (platform) {
      case "windows":     return n.endsWith(".exe")
      case "macos-arm":   return n.endsWith(".dmg") && n.includes("aarch64")
      case "macos-intel": return n.endsWith(".dmg") && n.includes("x86_64")
      case "linux":       return n.endsWith(".appimage") && n.includes("x86_64")
      case "linux-arm":   return n.endsWith(".appimage") && n.includes("aarch64")
    }
  })
}

function detectPlatform(): Platform {
  const ua = navigator.userAgent.toLowerCase()
  if (ua.includes("win")) return "windows"
  if (ua.includes("mac")) {
    // Best-effort: browsers don't reliably expose Apple Silicon vs Intel.
    return "macos-arm"
  }
  return "linux"
}

export default function DownloadPage() {
  const { dark } = useContext(ThemeContext)
  const [release, setRelease] = useState<ReleaseInfo | null>(null)
  const [error, setError] = useState("")
  const [loading, setLoading] = useState(true)
  const recommended = detectPlatform()

  useEffect(() => {
    fetch(`https://api.github.com/repos/${RELEASES_REPO}/releases/latest`)
      .then(r => {
        if (!r.ok) throw new Error(`GitHub API returned ${r.status}`)
        return r.json()
      })
      .then(setRelease)
      .catch(() => setError("Could not load the latest release right now — please try again shortly."))
      .finally(() => setLoading(false))
  }, [])

  const c = {
    text: dark ? "#e2e8f0" : "#1e293b",
    muted: dark ? "#94a3b8" : "#64748b",
    card: dark ? "#1e293b" : "#ffffff",
    border: dark ? "#334155" : "#e2e8f0",
    bg: dark ? "#0f172a" : "#f8fafc",
    accent: "#3b82f6",
  }

  const platforms: Platform[] = ["windows", "macos-arm", "macos-intel", "linux", "linux-arm"]

  return (
    <div style={{ background: c.bg, minHeight: "100vh", color: c.text, padding: "3rem 2rem" }}>
      <div style={{ maxWidth: "640px", margin: "0 auto" }}>
        <h1 style={{ marginBottom: "0.25rem" }}>Download BondQi</h1>
        <p style={{ color: c.muted, fontSize: "14px", marginBottom: "2rem" }}>
          The desktop app parses documents locally — PDFs never leave your machine for
          the LLM-mode extraction pipeline.
        </p>

        {loading && <p style={{ color: c.muted }}>Checking for the latest release…</p>}
        {error && (
          <div style={{ padding: "12px 16px", background: "rgba(220,38,38,.06)",
            border: "1px solid rgba(220,38,38,.3)", borderRadius: "8px", color: "#dc2626",
            fontSize: "13px", marginBottom: "1.5rem" }}>
            {error} You can also check releases directly on{" "}
            <a href={`https://github.com/${RELEASES_REPO}/releases`} style={{ color: "#dc2626" }}>GitHub</a>.
          </div>
        )}

        {release && (
          <>
            <p style={{ fontSize: "12px", color: c.muted, marginBottom: "1rem" }}>
              Latest version: <strong>{release.tag_name}</strong> · released{" "}
              {new Date(release.published_at).toLocaleDateString()}
            </p>

            <div style={{ display: "flex", flexDirection: "column", gap: "10px" }}>
              {platforms.map(p => {
                const asset = matchAsset(release.assets, p)
                const isRecommended = p === recommended
                return (
                  <div key={p} style={{
                    display: "flex", justifyContent: "space-between", alignItems: "center",
                    padding: "14px 18px", background: c.card,
                    border: `1px solid ${isRecommended ? c.accent : c.border}`,
                    borderRadius: "10px",
                  }}>
                    <div>
                      <div style={{ fontSize: "14px", fontWeight: 600 }}>
                        {PLATFORM_LABELS[p]}
                        {isRecommended && (
                          <span style={{ marginLeft: "8px", fontSize: "10px", fontWeight: 700,
                            color: c.accent, background: `${c.accent}15`, padding: "2px 8px",
                            borderRadius: "10px" }}>RECOMMENDED</span>
                        )}
                      </div>
                      {!asset && <div style={{ fontSize: "12px", color: c.muted }}>Not available in this release</div>}
                    </div>
                    {asset ? (
                      <a href={asset.browser_download_url} style={{
                        padding: "8px 18px", background: c.accent, color: "#fff",
                        borderRadius: "6px", textDecoration: "none", fontWeight: 600, fontSize: "13px",
                      }}>
                        Download
                      </a>
                    ) : (
                      <span style={{ padding: "8px 18px", color: c.muted, fontSize: "13px" }}>—</span>
                    )}
                  </div>
                )
              })}
            </div>

            <p style={{ fontSize: "11px", color: c.muted, marginTop: "1.5rem" }}>
              Installers are signed; the app checks for updates automatically after install.
            </p>
          </>
        )}
      </div>
    </div>
  )
}
