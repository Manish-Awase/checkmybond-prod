// frontend/src/pages/DashboardPage.tsx
import { useQuery } from "@tanstack/react-query"
import { getMonitoringStats } from "../lib/api"

// ── Sub-components ────────────────────────────────────────────────────────────

function StatCard({ label, value, color }: { label: string; value: string | number; color: string }) {
  return (
    <div style={{ padding:"16px", background:"#fff", borderRadius:"8px", border:"1px solid #e5e7eb" }}>
      <div style={{ fontSize:"24px", fontWeight:600, color }}>{value}</div>
      <div style={{ fontSize:"12px", color:"#6b7280", marginTop:"4px" }}>{label}</div>
    </div>
  )
}

function FailureBreakdown({ byFailureType }: { byFailureType: Record<string, number> }) {
  return (
    <div style={{ marginBottom:"2rem" }}>
      <h2 style={{ fontSize:"15px", marginBottom:"12px" }}>Failure Type Distribution</h2>
      <div style={{ display:"grid", gridTemplateColumns:"repeat(auto-fit,minmax(140px,1fr))", gap:"8px" }}>
        {Object.entries(byFailureType).map(([ft, count]) => (
          <div key={ft} style={{ padding:"10px 12px", background:"#fafafa",
            borderRadius:"6px", border:"1px solid #e5e7eb" }}>
            <div style={{ fontSize:"18px", fontWeight:600, color:"#374151" }}>{count}</div>
            <div style={{ fontSize:"11px", color:"#6b7280" }}>{ft.replace(/_/g, " ")}</div>
          </div>
        ))}
      </div>
    </div>
  )
}

function DriftTable({ drift }: { drift: any[] }) {
  return (
    <div style={{ marginBottom:"2rem" }}>
      <h2 style={{ fontSize:"15px", marginBottom:"12px" }}>Drift Report (7-day window)</h2>
      <table style={{ width:"100%", borderCollapse:"collapse", fontSize:"13px" }}>
        <thead>
          <tr style={{ background:"#f9fafb" }}>
            {["Field", "Recent", "Baseline", "Drift", "Status"].map(h => (
              <th key={h} style={{ padding:"8px 12px", textAlign:"left",
                borderBottom:"1px solid #e5e7eb", fontWeight:500 }}>
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {drift.map((d: any) => (
            <tr key={d.field_name} style={{ borderBottom:"1px solid #f3f4f6" }}>
              <td style={{ padding:"8px 12px", fontWeight:500 }}>{d.field_name}</td>
              <td style={{ padding:"8px 12px" }}>{(d.recent_avg * 100).toFixed(1)}%</td>
              <td style={{ padding:"8px 12px" }}>{(d.baseline_avg * 100).toFixed(1)}%</td>
              <td style={{ padding:"8px 12px", color: d.alert ? "#dc2626" : "#16a34a" }}>
                {d.drift > 0 ? "-" : "+"}{Math.abs(d.drift * 100).toFixed(1)}%
              </td>
              <td style={{ padding:"8px 12px" }}>
                {d.alert
                  ? <span style={{ color:"#dc2626", fontWeight:500 }}>⚠ Drift Alert</span>
                  : <span style={{ color:"#16a34a" }}>✓ Stable</span>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function PromptPerformance({ byPromptId }: { byPromptId: Record<string, number> }) {
  return (
    <div>
      <h2 style={{ fontSize:"15px", marginBottom:"12px" }}>Prompt Version Performance</h2>
      <div style={{ display:"grid", gridTemplateColumns:"repeat(auto-fit,minmax(200px,1fr))", gap:"8px" }}>
        {Object.entries(byPromptId).map(([pid, count]) => (
          <div key={pid} style={{ padding:"10px 12px", background:"#fafafa",
            borderRadius:"6px", border:"1px solid #e5e7eb" }}>
            <div style={{ fontSize:"12px", fontFamily:"monospace", color:"#3C3489", marginBottom:"4px" }}>
              {pid}
            </div>
            <div style={{ fontSize:"18px", fontWeight:600 }}>{count}</div>
            <div style={{ fontSize:"11px", color:"#6b7280" }}>corrections</div>
          </div>
        ))}
      </div>
    </div>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

export default function DashboardPage() {
  const { data, isLoading } = useQuery({
    queryKey: ["monitoring"],
    queryFn:  () => getMonitoringStats(),
    refetchInterval: 30_000,
  })

  if (isLoading) return <div style={{ padding:"2rem" }}>Loading dashboard...</div>

  const stats       = data?.correction_stats || {}
  const drift       = data?.drift_report     || []
  const qDepth      = data?.queue_depth      || 0
  const escalations = data?.escalations      || 0

  const summaryCards = [
    { label:"Total Corrections", value: stats.total || 0,                                                              color:"#3C3489"  },
    { label:"Avg Confidence",    value: stats.avg_confidence ? (stats.avg_confidence * 100).toFixed(1) + "%" : "—",   color:"#085041"  },
    { label:"Queue Depth",       value: qDepth,                                                                        color: qDepth > 10 ? "#dc2626" : "#633806" },
    { label:"DPO Pairs Ready",   value: stats.dpo_pairs_ready || 0,                                                    color:"#0C447C"  },
    { label:"SLA Escalations",   value: escalations,                                                                   color: escalations > 0 ? "#dc2626" : "#374151" },
  ]

  return (
    <div style={{ maxWidth:"1000px", margin:"0 auto", padding:"2rem" }}>
      <h1 style={{ marginBottom:"2rem" }}>Monitoring Dashboard</h1>

      {/* Summary cards */}
      <div style={{ display:"grid", gridTemplateColumns:"repeat(4,1fr)", gap:"12px", marginBottom:"2rem" }}>
        {summaryCards.map(c => (
          <StatCard key={c.label} label={c.label} value={c.value} color={c.color} />
        ))}
      </div>

      {stats.by_failure_type && Object.keys(stats.by_failure_type).length > 0 && (
        <FailureBreakdown byFailureType={stats.by_failure_type} />
      )}

      {drift.length > 0 && (
        <DriftTable drift={drift} />
      )}

      {stats.by_prompt_id && Object.keys(stats.by_prompt_id).length > 0 && (
        <PromptPerformance byPromptId={stats.by_prompt_id} />
      )}
    </div>
  )
}
