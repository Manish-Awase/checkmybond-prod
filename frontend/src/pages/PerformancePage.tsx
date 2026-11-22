// frontend/src/pages/PerformancePage.tsx
// Performance dashboard — covers all 6 workflows from ARCHITECTURE.md
// Workflow A: Login/Register      → audit_log (POST /api/auth/login)
// Workflow B: Upload→Extraction   → audit_log (upload) + extraction_jobs (queue wait + processing) + performance_logs (Python internals)
// Workflow C: Poll Extraction     → audit_log (GET /api/extractions/:id)
// Workflow D: Correction→DPO     → audit_log (POST /api/review/correct)
// Workflow E: Shutdown/Recovery   → not measurable at runtime
// Workflow F: Logout/Blacklist    → audit_log (POST /api/auth/logout)

import { useContext, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  getPerformanceWorkflows,
  getExtractionFunnel,
  getPipelineSummary,
  getRecentJobs,
  getPipelineTrace,
} from "../lib/api"
import { ThemeContext } from "../App"

// ── Types ─────────────────────────────────────────────────────────────────────

interface WorkflowRow {
  workflow:   string
  method:     string
  path:       string
  avg_ms:     number
  p95_ms:     number
  max_ms:     number
  call_count: number
  error_rate: number
}

interface ExtractionFunnel {
  total_jobs:        number
  complete:          number
  pending:           number
  failed:            number
  processing:        number
  success_rate:      number
  avg_queue_wait_ms: number
  p95_queue_wait_ms: number
  avg_processing_ms: number
  p95_processing_ms: number
  avg_total_ms:      number
}

interface PipelineRow {
  stage:      string
  step:       string
  avg_ms:     number
  p95_ms:     number
  max_ms:     number
  call_count: number
}

interface TraceStep {
  stage:       string
  step:        string
  field_name:  string | null
  duration_ms: number
  recorded_at: string
}

interface TraceResult {
  extraction_id: string
  steps:         TraceStep[]
  total_ms:      number
}

interface RecentJob {
  extraction_id: string
  status:        string
  queue_wait_ms: number
  processing_ms: number
  created_at:    string
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function msLabel(ms: number): string {
  if (!ms || ms === 0) return "—"
  if (ms >= 60000) return `${(ms / 60000).toFixed(1)}m`
  if (ms >= 1000)  return `${(ms / 1000).toFixed(1)}s`
  return `${Math.round(ms)}ms`
}

function msColor(ms: number, warn = 1000, crit = 3000): string {
  if (!ms || ms === 0) return "#94a3b8"
  if (ms >= crit) return "#ef4444"
  if (ms >= warn) return "#f59e0b"
  return "#22c55e"
}

const STAGE_COLORS: Record<string, { bar: string; bg: string; text: string }> = {
  pipeline:   { bar: "#3b82f6", bg: "rgba(59,130,246,.12)",  text: "#3b82f6" },
  extraction: { bar: "#8b5cf6", bg: "rgba(139,92,246,.12)",  text: "#8b5cf6" },
  evaluation: { bar: "#f59e0b", bg: "rgba(245,158,11,.12)",  text: "#f59e0b" },
  gate:       { bar: "#22c55e", bg: "rgba(34,197,94,.12)",   text: "#22c55e" },
}

function stageColor(stage: string) {
  return STAGE_COLORS[stage] ?? { bar: "#94a3b8", bg: "rgba(148,163,184,.12)", text: "#94a3b8" }
}

const WORKFLOW_COLORS: Record<string, string> = {
  "A: Login":               "#3b82f6",
  "A: Register":            "#60a5fa",
  "B: Upload → Extraction": "#8b5cf6",
  "C: Poll Extraction":     "#22c55e",
  "D: Correction → DPO":    "#f59e0b",
  "F: Logout / Blacklist":  "#94a3b8",
}

function workflowColor(name: string): string {
  for (const [k, v] of Object.entries(WORKFLOW_COLORS)) {
    if (name.startsWith(k.split(":")[0])) return v
  }
  return "#94a3b8"
}

const STATUS_COLORS: Record<string, string> = {
  complete:   "#22c55e",
  pending:    "#f59e0b",
  failed:     "#ef4444",
  processing: "#3b82f6",
}

// ── Sub-components ────────────────────────────────────────────────────────────

function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <h2 style={{
      fontSize: "11px", fontWeight: 700, textTransform: "uppercase",
      letterSpacing: ".07em", color: "#64748b", margin: "0 0 16px",
    }}>
      {children}
    </h2>
  )
}

function StatCard({ label, value, color, sub }: { label: string; value: string; color?: string; sub?: string }) {
  return (
    <div style={{
      background: "var(--card)", border: "1px solid var(--border)",
      borderRadius: "10px", padding: "14px 16px",
      display: "flex", flexDirection: "column", gap: "4px",
    }}>
      <span style={{
        fontSize: "20px", fontWeight: 700,
        fontFamily: "'IBM Plex Mono', monospace",
        color: color ?? "var(--text)",
      }}>
        {value}
      </span>
      <span style={{ fontSize: "10px", color: "#64748b", textTransform: "uppercase", letterSpacing: ".05em" }}>
        {label}
      </span>
      {sub && <span style={{ fontSize: "10px", color: "#94a3b8" }}>{sub}</span>}
    </div>
  )
}

function Card({ children, style }: { children: React.ReactNode; style?: React.CSSProperties }) {
  return (
    <div style={{
      background: "var(--card)", border: "1px solid var(--border)",
      borderRadius: "12px", padding: "20px",
      ...style,
    }}>
      {children}
    </div>
  )
}

function WorkflowBar({ rows, maxMs }: { rows: WorkflowRow[]; maxMs: number }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "10px" }}>
      {rows.map(r => {
        const pct   = maxMs > 0 ? (r.avg_ms / maxMs) * 100 : 0
        const p95p  = maxMs > 0 ? (r.p95_ms / maxMs) * 100 : 0
        const color = workflowColor(r.workflow)
        return (
          <div key={`${r.method}${r.path}`}>
            <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: "4px" }}>
              <div style={{ display: "flex", alignItems: "center", gap: "8px", minWidth: 0 }}>
                <span style={{
                  fontSize: "10px", fontWeight: 700, padding: "1px 6px",
                  borderRadius: "4px", flexShrink: 0,
                  background: color + "20", color,
                }}>
                  {r.workflow.split(":")[0]}
                </span>
                <span style={{
                  fontSize: "11px", color: "var(--text)",
                  fontFamily: "monospace",
                  overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
                }}>
                  {r.workflow.split(": ")[1] ?? r.path}
                </span>
              </div>
              <div style={{ display: "flex", gap: "14px", alignItems: "center", flexShrink: 0, marginLeft: "12px" }}>
                <span style={{ fontSize: "11px", color: "#64748b" }}>
                  avg <strong style={{ color: msColor(r.avg_ms) }}>{msLabel(r.avg_ms)}</strong>
                </span>
                <span style={{ fontSize: "11px", color: "#64748b" }}>
                  p95 <strong style={{ color: msColor(r.p95_ms, 2000, 5000) }}>{msLabel(r.p95_ms)}</strong>
                </span>
                {r.error_rate > 0 && (
                  <span style={{ fontSize: "10px", color: "#ef4444", fontWeight: 700 }}>
                    {r.error_rate}% err
                  </span>
                )}
                <span style={{ fontSize: "10px", color: "#94a3b8" }}>×{r.call_count}</span>
              </div>
            </div>
            <div style={{ position: "relative", height: "8px", background: "var(--bg)", borderRadius: "4px", overflow: "hidden" }}>
              <div style={{
                position: "absolute", left: 0, top: 0, bottom: 0,
                width: `${pct}%`, background: color,
                borderRadius: "4px", transition: "width .4s",
              }} />
              <div style={{
                position: "absolute", top: 0, bottom: 0,
                left: `${p95p}%`, width: "2px",
                background: "rgba(0,0,0,.25)",
              }} />
            </div>
          </div>
        )
      })}
    </div>
  )
}

function PipelineBar({ rows, maxMs }: { rows: PipelineRow[]; maxMs: number }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
      {rows.map(r => {
        const c    = stageColor(r.stage)
        const pct  = maxMs > 0 ? (r.avg_ms / maxMs) * 100 : 0
        const p95p = maxMs > 0 ? (r.p95_ms / maxMs) * 100 : 0
        return (
          <div key={`${r.stage}.${r.step}`}>
            <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: "3px" }}>
              <div style={{ display: "flex", alignItems: "center", gap: "6px" }}>
                <span style={{
                  fontSize: "9px", fontWeight: 700, textTransform: "uppercase",
                  letterSpacing: ".05em", padding: "1px 5px", borderRadius: "3px",
                  background: c.bg, color: c.text, flexShrink: 0,
                }}>
                  {r.stage}
                </span>
                <span style={{ fontSize: "11px", color: "var(--text)", fontFamily: "monospace" }}>
                  {r.step}
                </span>
              </div>
              <div style={{ display: "flex", gap: "12px", flexShrink: 0, marginLeft: "8px" }}>
                <span style={{ fontSize: "11px", color: "#64748b" }}>
                  avg <strong style={{ color: msColor(r.avg_ms) }}>{msLabel(r.avg_ms)}</strong>
                </span>
                <span style={{ fontSize: "11px", color: "#64748b" }}>
                  p95 <strong style={{ color: msColor(r.p95_ms) }}>{msLabel(r.p95_ms)}</strong>
                </span>
                <span style={{ fontSize: "10px", color: "#94a3b8" }}>×{r.call_count}</span>
              </div>
            </div>
            <div style={{ position: "relative", height: "8px", background: "var(--bg)", borderRadius: "3px", overflow: "hidden" }}>
              <div style={{
                position: "absolute", left: 0, top: 0, bottom: 0,
                width: `${pct}%`, background: c.bar,
                borderRadius: "3px", transition: "width .4s",
              }} />
              <div style={{
                position: "absolute", top: 0, bottom: 0,
                left: `${p95p}%`, width: "2px", background: "rgba(0,0,0,.2)",
              }} />
            </div>
          </div>
        )
      })}
    </div>
  )
}

function TraceWaterfall({ steps, totalMs }: { steps: TraceStep[]; totalMs: number }) {
  let cursor = 0
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "4px" }}>
      {steps.map((s, i) => {
        const c    = stageColor(s.stage)
        const pct  = totalMs > 0 ? (s.duration_ms / totalMs) * 100 : 0
        const left = totalMs > 0 ? (cursor / totalMs) * 100 : 0
        cursor += s.duration_ms
        return (
          <div key={i}>
            <div style={{ display: "flex", alignItems: "center", gap: "8px", marginBottom: "2px" }}>
              <span style={{
                fontSize: "9px", fontWeight: 700, textTransform: "uppercase",
                padding: "1px 5px", borderRadius: "3px",
                background: c.bg, color: c.text, flexShrink: 0,
              }}>
                {s.stage}
              </span>
              <span style={{ fontSize: "11px", color: "var(--text)", fontFamily: "monospace", flexShrink: 0 }}>
                {s.step}
              </span>
              {s.field_name && (
                <span style={{ fontSize: "10px", color: "#64748b" }}>({s.field_name})</span>
              )}
              <span style={{
                fontSize: "11px", marginLeft: "auto", flexShrink: 0, fontFamily: "monospace",
                color: msColor(s.duration_ms),
              }}>
                {msLabel(s.duration_ms)}
              </span>
            </div>
            <div style={{ position: "relative", height: "7px", background: "var(--bg)", borderRadius: "3px", overflow: "hidden" }}>
              <div style={{
                position: "absolute", top: 0, bottom: 0,
                left: `${left}%`, width: `${Math.max(pct, 0.5)}%`,
                background: c.bar, borderRadius: "3px",
              }} />
            </div>
          </div>
        )
      })}
      <div style={{
        marginTop: "8px", display: "flex", justifyContent: "flex-end",
        fontSize: "12px", fontWeight: 700, color: "var(--text)",
        fontFamily: "monospace",
      }}>
        Total: {msLabel(totalMs)}
      </div>
    </div>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

export default function PerformancePage() {
  const { dark } = useContext(ThemeContext)
  const [traceId, setTraceId] = useState("")
  const [inputId, setInputId] = useState("")

  const css = {
    "--bg":     dark ? "#0f172a" : "#f1f5f9",
    "--card":   dark ? "#1e293b" : "#ffffff",
    "--border": dark ? "#334155" : "#e2e8f0",
    "--text":   dark ? "#e2e8f0" : "#1e293b",
  } as React.CSSProperties

  const workflowQ = useQuery<WorkflowRow[]>({
    queryKey: ["perf-workflows"],
    queryFn:  getPerformanceWorkflows,
    refetchInterval: 30_000,
  })

  const funnelQ = useQuery<ExtractionFunnel>({
    queryKey: ["perf-funnel"],
    queryFn:  getExtractionFunnel,
    refetchInterval: 30_000,
  })

  const pipelineQ = useQuery<PipelineRow[]>({
    queryKey: ["perf-pipeline"],
    queryFn:  getPipelineSummary,
    refetchInterval: 30_000,
  })

  const recentQ = useQuery<RecentJob[]>({
    queryKey: ["perf-recent"],
    queryFn:  getRecentJobs,
    refetchInterval: 30_000,
  })

  const traceQ = useQuery<TraceResult>({
    queryKey: ["perf-trace", traceId],
    queryFn:  () => getPipelineTrace(traceId),
    enabled:  !!traceId,
  })

  const workflows = workflowQ.data ?? []
  const funnel    = funnelQ.data
  const pipeline  = pipelineQ.data ?? []
  const recent    = recentQ.data ?? []
  const maxWfMs   = workflows.reduce((m, r) => Math.max(m, r.avg_ms), 0)
  const maxPlMs   = pipeline.reduce((m, r) => Math.max(m, r.avg_ms), 0)

  const noData = (q: { isLoading: boolean; data?: any[] }) =>
    !q.isLoading && (!q.data || q.data.length === 0)

  return (
    <div style={{
      ...css,
      padding: "32px",
      fontFamily: "'Sora', sans-serif",
      color: "var(--text)",
      minHeight: "calc(100vh - 54px)",
      background: "var(--bg)",
      transition: "background .2s, color .2s",
    }}>

      {/* Header */}
      <div style={{ marginBottom: "28px" }}>
        <h1 style={{
          fontSize: "26px", fontWeight: 700, margin: "0 0 4px",
          background: "linear-gradient(135deg, #60a5fa, #a78bfa)",
          WebkitBackgroundClip: "text", WebkitTextFillColor: "transparent",
        }}>
          Performance
        </h1>
        <p style={{ fontSize: "13px", color: "#64748b", margin: 0 }}>
          All 6 workflows from ARCHITECTURE.md — last 7 days
        </p>
      </div>

      {/* ── Section 1: Workflow A/C/D/F — API timing from audit_log ── */}
      <Card style={{ marginBottom: "20px" }}>
        <SectionTitle>
          Workflows A · C · D · F — API response times (from audit_log)
        </SectionTitle>
        {workflowQ.isLoading && <div style={{ color: "#64748b", fontSize: "13px" }}>Loading...</div>}
        {noData(workflowQ) && (
          <div style={{ color: "#64748b", fontSize: "13px" }}>
            No audit_log data yet — make some API calls first.
          </div>
        )}
        {workflows.length > 0 && <WorkflowBar rows={workflows} maxMs={maxWfMs} />}
      </Card>

      {/* ── Section 2: Workflow B — Extraction funnel ── */}
      {funnel && (
        <>
          <div style={{ marginBottom: "8px" }}>
            <SectionTitle>Workflow B — Upload → Extraction funnel (from extraction_jobs)</SectionTitle>
          </div>
          <div style={{
            display: "grid",
            gridTemplateColumns: "repeat(auto-fit, minmax(130px, 1fr))",
            gap: "12px", marginBottom: "20px",
          }}>
            <StatCard label="Total jobs"      value={String(funnel.total_jobs)} />
            <StatCard label="Success rate"    value={`${funnel.success_rate}%`}
              color={funnel.success_rate >= 90 ? "#22c55e" : funnel.success_rate >= 70 ? "#f59e0b" : "#ef4444"} />
            <StatCard label="Complete"        value={String(funnel.complete)}   color="#22c55e" />
            <StatCard label="Pending review"  value={String(funnel.pending)}    color="#f59e0b" />
            <StatCard label="Failed"          value={String(funnel.failed)}     color={funnel.failed > 0 ? "#ef4444" : "#22c55e"} />
            <StatCard label="Avg queue wait"  value={msLabel(funnel.avg_queue_wait_ms)}
              color={msColor(funnel.avg_queue_wait_ms, 5000, 30000)}
              sub={`p95 ${msLabel(funnel.p95_queue_wait_ms)}`} />
            <StatCard label="Avg processing"  value={msLabel(funnel.avg_processing_ms)}
              color={msColor(funnel.avg_processing_ms, 10000, 60000)}
              sub={`p95 ${msLabel(funnel.p95_processing_ms)}`} />
            <StatCard label="Avg end-to-end"  value={msLabel(funnel.avg_total_ms)}
              color={msColor(funnel.avg_total_ms, 15000, 90000)} />
          </div>
        </>
      )}

      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "20px", marginBottom: "20px" }}>

        {/* ── Section 3: Workflow B Python internals ── */}
        <Card>
          <SectionTitle>Workflow B — Python pipeline internals (from performance_logs)</SectionTitle>
          {pipelineQ.isLoading && <div style={{ color: "#64748b", fontSize: "13px" }}>Loading...</div>}
          {noData(pipelineQ) && (
            <div style={{ color: "#64748b", fontSize: "13px" }}>
              No performance_logs yet — needs at least one completed extraction with timing instrumentation.
            </div>
          )}
          {pipeline.length > 0 && <PipelineBar rows={pipeline} maxMs={maxPlMs} />}
        </Card>

        {/* ── Section 4: Recent jobs ── */}
        <Card>
          <SectionTitle>Recent extraction jobs — click to trace</SectionTitle>
          {recentQ.isLoading && <div style={{ color: "#64748b", fontSize: "13px" }}>Loading...</div>}
          {noData(recentQ) && (
            <div style={{ color: "#64748b", fontSize: "13px" }}>No jobs yet.</div>
          )}
          <div style={{ display: "flex", flexDirection: "column", gap: "5px" }}>
            {recent.map(j => (
              <div key={j.extraction_id} style={{
                display: "flex", alignItems: "center", gap: "8px",
                padding: "7px 10px", borderRadius: "8px",
                background: "var(--bg)", border: "1px solid var(--border)",
              }}>
                <span style={{
                  fontSize: "9px", fontWeight: 700, textTransform: "uppercase",
                  padding: "1px 5px", borderRadius: "3px",
                  background: (STATUS_COLORS[j.status] ?? "#94a3b8") + "20",
                  color: STATUS_COLORS[j.status] ?? "#94a3b8",
                  flexShrink: 0,
                }}>
                  {j.status}
                </span>
                <span style={{
                  fontSize: "10px", fontFamily: "monospace", color: "#3b82f6",
                  flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
                }}>
                  {j.extraction_id.slice(0, 16)}…
                </span>
                <span style={{ fontSize: "10px", color: "#64748b", flexShrink: 0 }}>
                  wait {msLabel(j.queue_wait_ms)}
                </span>
                <span style={{
                  fontSize: "10px", flexShrink: 0,
                  color: msColor(j.processing_ms, 10000, 60000),
                  fontFamily: "monospace",
                }}>
                  {msLabel(j.processing_ms)}
                </span>
                <button
                  onClick={() => { setInputId(j.extraction_id); setTraceId(j.extraction_id) }}
                  style={{
                    background: "none", border: "1px solid var(--border)",
                    borderRadius: "5px", color: "#3b82f6",
                    fontSize: "10px", padding: "2px 7px",
                    cursor: "pointer", flexShrink: 0,
                    fontFamily: "'Sora', sans-serif",
                  }}
                >
                  Trace →
                </button>
              </div>
            ))}
          </div>
        </Card>
      </div>

      {/* ── Section 5: Waterfall trace ── */}
      <Card>
        <SectionTitle>Workflow B — waterfall trace for one extraction</SectionTitle>
        <div style={{ display: "flex", gap: "10px", marginBottom: "20px" }}>
          <input
            value={inputId}
            onChange={e => setInputId(e.target.value)}
            placeholder="Paste extraction_id or click Trace → above"
            style={{
              flex: 1, padding: "8px 12px",
              background: "var(--bg)", color: "var(--text)",
              border: "1px solid var(--border)",
              borderRadius: "8px", fontSize: "12px",
              fontFamily: "'IBM Plex Mono', monospace",
              outline: "none",
            }}
            onFocus={e  => e.currentTarget.style.borderColor = "#3b82f6"}
            onBlur={e   => e.currentTarget.style.borderColor = "var(--border)"}
            onKeyDown={e => e.key === "Enter" && setTraceId(inputId)}
          />
          <button
            onClick={() => setTraceId(inputId)}
            style={{
              background: "linear-gradient(135deg, #2563eb, #1d4ed8)",
              border: "none", borderRadius: "8px", color: "white",
              fontFamily: "'Sora', sans-serif", fontSize: "13px", fontWeight: 700,
              padding: "8px 18px", cursor: "pointer",
            }}
          >
            Load
          </button>
        </div>

        {traceQ.isLoading && <div style={{ color: "#64748b", fontSize: "13px" }}>Loading trace...</div>}

        {traceQ.data && traceQ.data.steps.length > 0 && (
          <>
            {/* Legend */}
            <div style={{ display: "flex", gap: "12px", marginBottom: "16px", flexWrap: "wrap" }}>
              {Object.entries(STAGE_COLORS).map(([stage, c]) => (
                <div key={stage} style={{ display: "flex", alignItems: "center", gap: "5px" }}>
                  <div style={{ width: "9px", height: "9px", borderRadius: "2px", background: c.bar }} />
                  <span style={{ fontSize: "11px", color: "#64748b" }}>{stage}</span>
                </div>
              ))}
              <div style={{ display: "flex", alignItems: "center", gap: "5px" }}>
                <div style={{ width: "9px", height: "9px", borderRadius: "2px", background: "#22c55e" }} />
                <span style={{ fontSize: "11px", color: "#64748b" }}>fast (&lt;1s)</span>
              </div>
              <div style={{ display: "flex", alignItems: "center", gap: "5px" }}>
                <div style={{ width: "9px", height: "9px", borderRadius: "2px", background: "#f59e0b" }} />
                <span style={{ fontSize: "11px", color: "#64748b" }}>slow (&gt;1s)</span>
              </div>
              <div style={{ display: "flex", alignItems: "center", gap: "5px" }}>
                <div style={{ width: "9px", height: "9px", borderRadius: "2px", background: "#ef4444" }} />
                <span style={{ fontSize: "11px", color: "#64748b" }}>critical (&gt;3s)</span>
              </div>
            </div>
            <TraceWaterfall steps={traceQ.data.steps} totalMs={traceQ.data.total_ms} />
          </>
        )}

        {traceQ.data && traceQ.data.steps.length === 0 && (
          <div style={{ color: "#64748b", fontSize: "13px" }}>
            No timing data found — this extraction ran before instrumentation was added, or timing flush failed.
          </div>
        )}

        {!traceId && (
          <div style={{ color: "#94a3b8", fontSize: "12px" }}>
            Enter an extraction_id above or click "Trace →" from the recent jobs list.
          </div>
        )}
      </Card>

      <style>{`
        @import url('https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;600&family=Sora:wght@400;600;700&display=swap');
      `}</style>
    </div>
  )
}