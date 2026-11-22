// frontend/src/pages/AdminPage.tsx
import { useState, useMemo, useContext } from "react"
import { useQuery, useMutation } from "@tanstack/react-query"
import { useNavigate } from "react-router-dom"
import { getQueueItems, getAllJobs, clearOverdueJobs } from "../lib/api"
import { ThemeContext } from "../App"

// ── Types ────────────────────────────────────────────────────────────────────
type JobStatus = "pending" | "processing" | "complete" | "failed"

interface QueueItem {
  id:           string
  doc_hash:     string
  schema_id:    string | null
  status:       JobStatus
  pdf_path:     string | null
  created_at:   string
  completed_at: string | null
  sla_deadline: string
}

// ── Helpers ──────────────────────────────────────────────────────────────────
function fmt(iso: string | null): string {
  if (!iso) return "—"
  return new Date(iso).toLocaleString(undefined, {
    month: "short", day: "numeric",
    hour: "2-digit", minute: "2-digit",
  })
}

function duration(created: string, completed: string | null): string {
  if (!completed) return "—"
  const ms = new Date(completed).getTime() - new Date(created).getTime()
  const s  = Math.floor(ms / 1000)
  if (s < 60)   return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}

function slaStatus(deadline: string, status: JobStatus): "ok" | "at_risk" | "breached" {
  if (status === "complete" || status === "failed") return "ok"
  const diff = new Date(deadline).getTime() - Date.now()
  if (diff < 0)         return "breached"
  if (diff < 3_600_000) return "at_risk"
  return "ok"
}

const STATUS_META: Record<JobStatus, { label: string; color: string; bg: string }> = {
  pending:    { label: "Pending",    color: "#94a3b8", bg: "rgba(148,163,184,.12)" },
  processing: { label: "Processing", color: "#60a5fa", bg: "rgba(96,165,250,.12)"  },
  complete:   { label: "Complete",   color: "#34d399", bg: "rgba(52,211,153,.12)"  },
  failed:     { label: "Failed",     color: "#f87171", bg: "rgba(248,113,113,.12)" },
}

const ALL_STATUSES: JobStatus[] = ["pending", "processing", "complete", "failed"]


// ── Component ────────────────────────────────────────────────────────────────
export default function AdminPage() {
  const navigate          = useNavigate()
  const { dark }          = useContext(ThemeContext)
  const [filter,  setFilter]  = useState<JobStatus | "all">("all")
  const [search,  setSearch]  = useState("")
  const [sort,    setSort]    = useState<"created_at" | "sla_deadline" | "status">("created_at")
  const [sortDir, setSortDir] = useState<"asc" | "desc">("desc")
  const [showConfirm, setShowConfirm] = useState(false)
  // ── Theme colors ──────────────────────────────────────────────
  const t = {
    bg:          dark ? "#0f172a"  : "#f1f5f9",
    card:        dark ? "#1e293b"  : "#ffffff",
    cardBorder:  dark ? "#334155"  : "#e2e8f0",
    text:        dark ? "#e2e8f0"  : "#1e293b",
    subtext:     dark ? "#94a3b8"  : "#64748b",
    dim:         dark ? "#475569"  : "#94a3b8",
    input:       dark ? "#1e293b"  : "#ffffff",
    inputBorder: dark ? "#334155"  : "#e2e8f0",
    inputText:   dark ? "#e2e8f0"  : "#1e293b",
    inputPH:     dark ? "#334155"  : "#cbd5e1",
    thead:       dark ? "#172032"  : "#f8fafc",
    theadBorder: dark ? "#334155"  : "#e2e8f0",
    rowHover:    dark ? "#243447"  : "#f8fafc",
    rowBorder:   dark ? "#1a2438"  : "#f1f5f9",
    pill:        dark ? "#1e293b"  : "#f1f5f9",
    pillBorder:  dark ? "#334155"  : "#e2e8f0",
    pillText:    dark ? "#64748b"  : "#64748b",
    pillActive:  dark ? "#1d3557"  : "#eff6ff",
    refreshBg:   dark ? "#1e293b"  : "#f8fafc",
    refreshBorder:dark? "#334155"  : "#e2e8f0",
    refreshText: dark ? "#94a3b8"  : "#64748b",
    openBtn:     dark ? "#1d3557"  : "#eff6ff",
  }

  const activeQuery = useQuery<QueueItem[]>({
    queryKey: ["queue-active"],
    queryFn:  getQueueItems,
    refetchInterval: 15_000,
  })

  const allQuery = useQuery<QueueItem[]>({
    queryKey: ["queue-all"],
    queryFn:  getAllJobs,
    refetchInterval: 30_000,
  })

  const clearOverdueMutation = useMutation({
    mutationFn: clearOverdueJobs,
    onSuccess:  () => {
      allQuery.refetch()
      activeQuery.refetch()
    },
  })

  const jobs = allQuery.data ?? []

  const stats = useMemo(() => {
    const total       = jobs.length
    const complete    = jobs.filter(j => j.status === "complete").length
    const failed      = jobs.filter(j => j.status === "failed").length
    const active      = jobs.filter(j => j.status === "pending" || j.status === "processing").length
    const breached    = jobs.filter(j => slaStatus(j.sla_deadline, j.status) === "breached").length
    const successRate = total > 0 ? Math.round((complete / total) * 100) : 0
    return { total, complete, failed, active, breached, successRate }
  }, [jobs])

  const visible = useMemo(() => {
    let list = [...jobs]
    if (filter !== "all") list = list.filter(j => j.status === filter)
    if (search.trim()) {
      const q = search.toLowerCase()
      list = list.filter(j =>
        j.id.toLowerCase().includes(q) ||
        j.doc_hash.toLowerCase().includes(q) ||
        (j.schema_id ?? "").toLowerCase().includes(q)
      )
    }
    list.sort((a, b) => {
      let va: string | number = a[sort] ?? ""
      let vb: string | number = b[sort] ?? ""
      if (sort === "created_at" || sort === "sla_deadline") {
        va = new Date(va as string).getTime()
        vb = new Date(vb as string).getTime()
      }
      if (va < vb) return sortDir === "asc" ? -1 : 1
      if (va > vb) return sortDir === "asc" ? 1  : -1
      return 0
    })
    return list
  }, [jobs, filter, search, sort, sortDir])

  function toggleSort(col: typeof sort) {
    if (sort === col) setSortDir(d => d === "asc" ? "desc" : "asc")
    else { setSort(col); setSortDir("desc") }
  }

  const isLoading = allQuery.isLoading

  return (
    <div style={{
      padding: "32px",
      fontFamily: "'Sora', sans-serif",
      color: t.text,
      minHeight: "calc(100vh - 54px)",
      background: t.bg,
      transition: "background .2s, color .2s",
    }}>

      {/* ── Header ── */}
      <div style={{ display:"flex", alignItems:"flex-start", justifyContent:"space-between", marginBottom:"28px", gap:"16px", flexWrap:"wrap" }}>
        <div>
          <h1 style={{
            fontSize:"26px", fontWeight:700, margin:"0 0 4px",
            background:"linear-gradient(135deg, #60a5fa, #a78bfa)",
            WebkitBackgroundClip:"text", WebkitTextFillColor:"transparent",
          }}>
            Admin Dashboard
          </h1>
          <p style={{ fontSize:"13px", color: t.dim, margin:0 }}>
            Full extraction job history &amp; SLA monitoring
          </p>
        </div>
        <button
          onClick={() => { allQuery.refetch(); activeQuery.refetch() }}
          style={{
            display:"flex", alignItems:"center", gap:"6px",
            background: t.refreshBg, border:`1px solid ${t.refreshBorder}`,
            borderRadius:"8px", color: t.refreshText,
            fontFamily:"'Sora', sans-serif", fontSize:"13px",
            padding:"8px 14px", cursor:"pointer", transition:"all .15s",
          }}
        >
          ↻ Refresh
        </button>
      </div>
      {/* --adding clear overdue job button*/}
      <button
        onClick={() => setShowConfirm(true)}
        style={{
          display: "flex", alignItems: "center", gap: "6px",
          background: "rgba(239,68,68,.1)", border: "1px solid rgba(239,68,68,.3)",
          borderRadius: "8px", color: "#ef4444",
          fontFamily: "'Sora', sans-serif", fontSize: "13px",
          padding: "8px 14px", cursor: "pointer", transition: "all .15s",
        }}
      >
        🗑 Clear overdue jobs
      </button>    
      {/* ── Stats ── */}
      <div style={{ display:"grid", gridTemplateColumns:"repeat(auto-fit,minmax(110px,1fr))", gap:"12px", marginBottom:"24px" }}>
        {[
          { label:"Total jobs",   value: stats.total,            color: t.text },
          { label:"Active",       value: stats.active,           color: stats.active > 0    ? "#60a5fa" : t.text },
          { label:"Completed",    value: stats.complete,         color: "#34d399" },
          { label:"Failed",       value: stats.failed,           color: stats.failed > 0    ? "#f87171" : t.text },
          { label:"SLA breaches", value: stats.breached,         color: stats.breached > 0  ? "#f87171" : "#34d399" },
          { label:"Success rate", value:`${stats.successRate}%`, color: stats.successRate >= 90 ? "#34d399" : stats.successRate >= 70 ? "#fbbf24" : "#f87171" },
        ].map(s => (
          <div key={s.label} style={{
            background: t.card, border:`1px solid ${t.cardBorder}`,
            borderRadius:"10px", padding:"14px 16px",
            display:"flex", flexDirection:"column", gap:"4px",
            transition:"background .2s",
          }}>
            <span style={{ fontSize:"22px", fontWeight:700, fontFamily:"'IBM Plex Mono',monospace", color: s.color }}>
              {s.value}
            </span>
            <span style={{ fontSize:"11px", color: t.dim, textTransform:"uppercase", letterSpacing:".05em" }}>
              {s.label}
            </span>
          </div>
        ))}
      </div>

      {/* ── Toolbar ── */}
      <div style={{ display:"flex", gap:"12px", marginBottom:"20px", flexWrap:"wrap", alignItems:"center" }}>
        {/* Search */}
        <div style={{ position:"relative", flex:1, minWidth:"200px", maxWidth:"360px" }}>
          <span style={{ position:"absolute", left:"10px", top:"50%", transform:"translateY(-50%)", color: t.dim, fontSize:"16px", pointerEvents:"none" }}>⌕</span>
          <input
            placeholder="Search by ID, hash, schema…"
            value={search}
            onChange={e => setSearch(e.target.value)}
            style={{
              width:"100%", background: t.input,
              border:`1px solid ${t.inputBorder}`,
              borderRadius:"8px", color: t.inputText,
              fontFamily:"'IBM Plex Mono',monospace", fontSize:"12px",
              padding:"8px 32px 8px 30px", outline:"none",
              boxSizing:"border-box", transition:"border-color .15s",
            }}
          />
          {search && (
            <button onClick={() => setSearch("")} style={{
              position:"absolute", right:"8px", top:"50%", transform:"translateY(-50%)",
              background:"none", border:"none", color: t.dim, cursor:"pointer", fontSize:"11px",
            }}>✕</button>
          )}
        </div>

        {/* Filter pills */}
        <div style={{ display:"flex", gap:"6px", flexWrap:"wrap" }}>
          {(["all", ...ALL_STATUSES] as const).map(s => (
            <button
              key={s}
              onClick={() => setFilter(s)}
              style={{
                background: filter === s ? t.pillActive : t.pill,
                border:`1px solid ${filter === s ? "#3b82f6" : t.pillBorder}`,
                borderRadius:"20px",
                color: filter === s ? "#60a5fa" : t.pillText,
                fontFamily:"'Sora',sans-serif", fontSize:"11px",
                padding:"5px 12px", cursor:"pointer",
                transition:"all .15s", whiteSpace:"nowrap",
              }}
            >
              {s === "all"
                ? `All (${jobs.length})`
                : `${STATUS_META[s].label} (${jobs.filter(j => j.status === s).length})`}
            </button>
          ))}
        </div>
      </div>

      {/* ── Table ── */}
      <div style={{
        background: t.card, border:`1px solid ${t.cardBorder}`,
        borderRadius:"12px", overflow:"hidden",
        transition:"background .2s",
      }}>
        {isLoading ? (
          <div style={{ display:"flex", alignItems:"center", justifyContent:"center", gap:"12px", padding:"48px", color: t.dim, fontSize:"13px" }}>
            <div style={{ width:"24px", height:"24px", border:`2px solid ${t.cardBorder}`, borderTopColor:"#3b82f6", borderRadius:"50%", animation:"spin .7s linear infinite" }} />
            Loading job history…
          </div>
        ) : visible.length === 0 ? (
          <div style={{ display:"flex", flexDirection:"column", alignItems:"center", gap:"8px", padding:"56px", color: t.dim }}>
            <div style={{ fontSize:"28px" }}>◫</div>
            <p style={{ fontSize:"13px", margin:0 }}>No jobs match your filter</p>
          </div>
        ) : (
          <table style={{ width:"100%", borderCollapse:"collapse", fontSize:"12px" }}>
            <thead>
              <tr style={{ background: t.thead, borderBottom:`1px solid ${t.theadBorder}` }}>
                {[
                  { label:"Job ID",       col: null },
                  { label:"Doc hash",     col: null },
                  { label:"Schema",       col: null },
                  { label:"Status",       col: "status" },
                  { label:"Created",      col: "created_at" },
                  { label:"Duration",     col: null },
                  { label:"SLA deadline", col: "sla_deadline" },
                  { label:"SLA",          col: null },
                  { label:"",             col: null },
                ].map(({ label, col }) => (
                  <th
                    key={label}
                    onClick={() => col && toggleSort(col as any)}
                    style={{
                      padding:"11px 14px", textAlign:"left",
                      fontSize:"10px", fontWeight:600,
                      textTransform:"uppercase", letterSpacing:".06em",
                      color: t.dim, whiteSpace:"nowrap",
                      cursor: col ? "pointer" : "default",
                      userSelect:"none",
                    }}
                  >
                    {label} {col && sort === col ? (sortDir === "asc" ? "↑" : "↓") : ""}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {visible.map(job => {
                const sm  = STATUS_META[job.status]
                const sla = slaStatus(job.sla_deadline, job.status)
                return (
                  <tr key={job.id} style={{ borderBottom:`1px solid ${t.rowBorder}`, transition:"background .12s" }}
                    onMouseEnter={e => (e.currentTarget.style.background = t.rowHover)}
                    onMouseLeave={e => (e.currentTarget.style.background = "transparent")}
                  >
                    <td style={{ padding:"10px 14px", fontFamily:"monospace", color:"#60a5fa", fontSize:"11px" }}>
                      {job.id.slice(0, 8)}…
                    </td>
                    <td style={{ padding:"10px 14px", fontFamily:"monospace", color: t.subtext, fontSize:"11px" }}>
                      {job.doc_hash.slice(0, 10)}…
                    </td>
                    <td style={{ padding:"10px 14px", fontSize:"12px", color: t.subtext }}>
                      {job.schema_id ?? <em style={{ color: t.dim, fontStyle:"italic" }}>none</em>}
                    </td>
                    <td style={{ padding:"10px 14px" }}>
                      <span style={{ display:"inline-block", fontSize:"10px", fontWeight:600, textTransform:"uppercase", letterSpacing:".04em", padding:"3px 8px", borderRadius:"20px", color: sm.color, background: sm.bg }}>
                        {sm.label}
                      </span>
                    </td>
                    <td style={{ padding:"10px 14px", fontFamily:"monospace", color: t.dim, fontSize:"11px" }}>
                      {fmt(job.created_at)}
                    </td>
                    <td style={{ padding:"10px 14px", fontFamily:"monospace", color: t.dim, fontSize:"11px" }}>
                      {duration(job.created_at, job.completed_at)}
                    </td>
                    <td style={{ padding:"10px 14px", fontFamily:"monospace", color: t.dim, fontSize:"11px" }}>
                      {fmt(job.sla_deadline)}
                    </td>
                    <td style={{ padding:"10px 14px" }}>
                      {sla === "breached" && <span style={{ fontSize:"10px", fontWeight:700, textTransform:"uppercase", letterSpacing:".05em", padding:"3px 8px", borderRadius:"20px", color:"#f87171", background:"rgba(248,113,113,.1)" }}>Breached</span>}
                      {sla === "at_risk"  && <span style={{ fontSize:"10px", fontWeight:700, textTransform:"uppercase", letterSpacing:".05em", padding:"3px 8px", borderRadius:"20px", color:"#fbbf24", background:"rgba(251,191,36,.1)"  }}>At risk</span>}
                      {sla === "ok"       && <span style={{ fontSize:"10px", fontWeight:700, textTransform:"uppercase", letterSpacing:".05em", padding:"3px 8px", borderRadius:"20px", color:"#34d399", background:"rgba(52,211,153,.1)"  }}>OK</span>}
                    </td>
                    <td style={{ padding:"10px 14px" }}>
                      {(job.status === "pending") && (
                        <button
                          onClick={() => navigate(`/review/${job.id}`)}
                          style={{
                            background:"none", border:`1px solid ${t.cardBorder}`,
                            borderRadius:"6px", color:"#3b82f6",
                            fontFamily:"'Sora',sans-serif", fontSize:"11px",
                            padding:"4px 10px", cursor:"pointer", transition:"all .15s",
                          }}
                        >
                          Open →
                        </button>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>

      {/* ── Count ── */}
      {!isLoading && (
        <p style={{ fontSize:"11px", color: t.dim, margin:"10px 0 0", fontFamily:"'IBM Plex Mono',monospace" }}>
          Showing {visible.length} of {jobs.length} jobs
        </p>
      )}
      {/* ── logic of delete overdue button  ── */}
      {showConfirm && (
        <div style={{
          position: "fixed", inset: 0, zIndex: 1000,
          background: "rgba(0,0,0,.5)",
          display: "flex", alignItems: "center", justifyContent: "center",
        }}>
          <div style={{
            background: t.card, border: `1px solid ${t.cardBorder}`,
            borderRadius: "14px", padding: "28px 32px",
            maxWidth: "380px", width: "100%",
            fontFamily: "'Sora', sans-serif",
            boxShadow: "0 24px 64px rgba(0,0,0,.3)",
          }}>
            <div style={{ fontSize: "28px", marginBottom: "12px" }}>⚠️</div>
            <h2 style={{ fontSize: "16px", fontWeight: 700, color: t.text, margin: "0 0 8px" }}>
              Clear overdue jobs?
            </h2>
            <p style={{ fontSize: "13px", color: t.subtext, margin: "0 0 24px", lineHeight: 1.5 }}>
              All pending and processing jobs past their SLA deadline will be marked as
              <strong style={{ color: "#ef4444" }}> failed</strong> and removed from the queue.
              This cannot be undone.
            </p>
            <div style={{ display: "flex", gap: "10px", justifyContent: "flex-end" }}>
              <button
                onClick={() => setShowConfirm(false)}
                style={{
                  background: t.card, border: `1px solid ${t.cardBorder}`,
                  borderRadius: "8px", color: t.subtext,
                  fontFamily: "'Sora', sans-serif", fontSize: "13px", fontWeight: 600,
                  padding: "9px 20px", cursor: "pointer",
                }}
              >
                No, cancel
              </button>
              <button
                onClick={() => {
                  setShowConfirm(false)
                  clearOverdueMutation.mutate()
                }}
                disabled={clearOverdueMutation.isPending}
                style={{
                  background: "linear-gradient(135deg, #dc2626, #b91c1c)",
                  border: "none", borderRadius: "8px", color: "white",
                  fontFamily: "'Sora', sans-serif", fontSize: "13px", fontWeight: 700,
                  padding: "9px 20px", cursor: "pointer",
                  boxShadow: "0 4px 12px rgba(220,38,38,.3)",
                }}
              >
                {clearOverdueMutation.isPending ? "Clearing…" : "Yes, clear all"}
              </button>
            </div>
          </div>
        </div>
      )}
      <style>{`
        @import url('https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=Sora:wght@400;500;600;700&display=swap');
        @keyframes spin { to { transform: rotate(360deg); } }
      `}</style>
    </div>
  )
}