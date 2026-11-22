// frontend/src/pages/PipelineMonitorPage.tsx
// Internal debug page — access at /pipeline (not in nav bar)
// Shows each job as a workflow trace matching ARCHITECTURE.md Workflow B

import { useState, useContext } from "react"
import { useQuery }             from "@tanstack/react-query"
import { getAllJobs }            from "../lib/api"
import { ThemeContext }          from "../App"
import { useAuth }               from "../context/AuthContext"

// ── Pipeline steps — exactly matching ARCHITECTURE.md Workflow B ──────────────

const PIPELINE_STEPS = [
  {
    id:       "queued",
    label:    "Queued",
    fn:       "iqueue.EnqueueExtractionJob()",
    layer:    "Go Server → Redis",
    detail:   "LPUSH to Redis DB1 in Celery wire format (base64 body, eta=null fixed)",
  },
  {
    id:       "picked_up",
    label:    "Picked Up",
    fn:       "on_task_start() → process_document()",
    layer:    "Celery Worker",
    detail:   "BRPOP from Redis → worker_init → job.status='processing', job.started_at=utcnow()",
  },
  {
    id:       "parse",
    label:    "1. Parse PDF",
    fn:       "ParserService.parse()",
    layer:    "services/parser_service.py",
    detail:   "pdfplumber digital extraction → pytesseract OCR fallback if text < threshold",
  },
  {
    id:       "chunk",
    label:    "2. Chunk",
    fn:       "Normalizer.chunk_document()",
    layer:    "services/normalizer.py",
    detail:   "Header-aware chunking → serialised for vector indexing",
  },
  {
    id:       "embed",
    label:    "3. Embed",
    fn:       "VectorStoreService.index_chunks()",
    layer:    "services/vector_store.py",
    detail:   "sentence-transformers → Qdrant persistent store (keyed by doc_hash)",
  },
  {
    id:       "extract",
    label:    "4. Extract Fields",
    fn:       "FieldExtractor.extract_field() × N",
    layer:    "services/extractor.py",
    detail:   "For each schema field: Groq/Anthropic LLM call with retrieved chunks",
  },
  {
    id:       "evaluate",
    label:    "5. Evaluate",
    fn:       "RAGEvaluator.evaluate()",
    layer:    "services/evaluator.py",
    detail:   "context_relevance · groundedness · answer_relevance · confidence_score",
  },
  {
    id:       "gate",
    label:    "6. Confidence Gate",
    fn:       "ConfidenceGate.decide()",
    layer:    "services/confidence_gate.py",
    detail:   "auto_accept | human_review | re_extract | fail → SLAQueue.enqueue()",
  },
  {
    id:       "done",
    label:    "Done",
    fn:       "job.result_json = json.dumps(field_results)",
    layer:    "workers/celery_app.py",
    detail:   "status='complete' (all auto_accept) or 'pending' (needs human review) → db.commit()",
  },
]

// ── Helpers ───────────────────────────────────────────────────────────────────

function inferStep(job: any): number {
  // Map DB state to the step index the job is currently at or failed on
  if (job.status === "failed") {
    // If there's an error, try to infer which step it failed on
    const err = (job.error || "").toLowerCase()
    if (err.includes("parser") || err.includes("pdfplumber") || err.includes("tesseract"))  return 2
    if (err.includes("normaliz") || err.includes("chunk"))                                   return 3
    if (err.includes("vector") || err.includes("qdrant") || err.includes("embed"))          return 4
    if (err.includes("extract") || err.includes("groq") || err.includes("anthropic") ||
        err.includes("connection error") || err.includes("llm"))                             return 5
    if (err.includes("evaluat") || err.includes("rag"))                                      return 6
    if (err.includes("confidence") || err.includes("gate") || err.includes("sla"))          return 7
    return 1  // unknown — failed during pickup
  }
  if (job.status === "pending" && !job.started_at) return 0   // waiting in Redis
  if (job.status === "processing")                  return 4   // mid-pipeline (best guess)
  if (job.status === "complete" ||
      (job.status === "pending" && job.started_at)) return 8   // done (pending = needs review)
  return 0
}

function fmt(ts: string | null): string {
  if (!ts) return "—"
  return new Date(ts).toLocaleTimeString()
}

function duration(a: string | null, b: string | null): string {
  if (!a || !b) return "—"
  const ms = new Date(b).getTime() - new Date(a).getTime()
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}

// ── Step node ─────────────────────────────────────────────────────────────────

function StepNode({
  step, index, currentStep, failed, dark,
}: {
  step: typeof PIPELINE_STEPS[0]
  index: number
  currentStep: number
  failed: boolean
  dark: boolean
}) {
  const [hover, setHover] = useState(false)

  const done    = index < currentStep && !failed
  const active  = index === currentStep
  const errored = failed && index === currentStep
  const waiting = index > currentStep

  const color = errored ? "#dc2626"
    : done    ? "#16a34a"
    : active  ? "#3b82f6"
    :           "#9ca3af"

  const bg = errored ? "rgba(220,38,38,.08)"
    : done    ? "rgba(22,163,74,.06)"
    : active  ? "rgba(59,130,246,.08)"
    :           dark ? "rgba(255,255,255,.03)" : "rgba(0,0,0,.02)"

  return (
    <div style={{ display:"flex", alignItems:"flex-start", gap:"12px", position:"relative" }}>

      {/* Connector line */}
      {index < PIPELINE_STEPS.length - 1 && (
        <div style={{
          position:   "absolute",
          left:       "15px",
          top:        "32px",
          width:      "2px",
          height:     "calc(100% + 8px)",
          background: done ? "#16a34a40" : "#e5e7eb",
          zIndex:     0,
        }} />
      )}

      {/* Circle */}
      <div style={{
        width:        "30px",
        height:       "30px",
        borderRadius: "50%",
        border:       `2px solid ${color}`,
        background:   errored || done || active ? bg : "transparent",
        display:      "flex",
        alignItems:   "center",
        justifyContent: "center",
        flexShrink:   0,
        zIndex:       1,
        fontSize:     "13px",
        fontWeight:   700,
        color,
        transition:   "all .2s",
      }}>
        {done ? "✓" : errored ? "✕" : active ? "●" : index + 1}
      </div>

      {/* Content */}
      <div
        onMouseEnter={() => setHover(true)}
        onMouseLeave={() => setHover(false)}
        style={{
          flex:         1,
          padding:      "8px 12px",
          borderRadius: "8px",
          border:       `1px solid ${active || errored ? color + "50" : "transparent"}`,
          background:   hover || active || errored ? bg : "transparent",
          marginBottom: "8px",
          cursor:       "default",
          transition:   "all .15s",
        }}>

        {/* Step label + function name */}
        <div style={{ display:"flex", alignItems:"baseline", gap:"8px", flexWrap:"wrap" }}>
          <span style={{
            fontSize:   "13px",
            fontWeight: active || errored ? 700 : done ? 600 : 400,
            color:      waiting ? "#9ca3af" : dark ? "#e2e8f0" : "#1e293b",
          }}>
            {step.label}
          </span>
          <code style={{
            fontSize:   "10px",
            padding:    "1px 6px",
            borderRadius: "4px",
            background: dark ? "#1e293b" : "#f1f5f9",
            color:      color,
            fontFamily: "monospace",
          }}>
            {step.fn}
          </code>
        </div>

        {/* Layer */}
        <div style={{
          fontSize: "11px",
          color:    "#6b7280",
          marginTop: "2px",
        }}>
          {step.layer}
        </div>

        {/* Detail — only show on hover or if active/error */}
        {(hover || active || errored) && (
          <div style={{
            fontSize:   "11px",
            color:      dark ? "#94a3b8" : "#64748b",
            marginTop:  "4px",
            lineHeight: 1.5,
          }}>
            {step.detail}
          </div>
        )}
      </div>
    </div>
  )
}

// ── Job card ──────────────────────────────────────────────────────────────────

function JobCard({ job, dark }: { job: any; dark: boolean }) {
  const [expanded, setExpanded] = useState(false)

  const currentStep = inferStep(job)
  const failed      = job.status === "failed"
  const isReview    = job.status === "pending" && job.started_at

  const statusColor: Record<string, string> = {
    pending:    "#f59e0b",
    processing: "#3b82f6",
    complete:   "#16a34a",
    failed:     "#dc2626",
  }
  const sc = statusColor[job.status] || "#6b7280"

  return (
    <div style={{
      background:   dark ? "#0f172a" : "#ffffff",
      border:       `1px solid ${failed ? "#dc262630" : dark ? "#1e293b" : "#e5e7eb"}`,
      borderRadius: "12px",
      marginBottom: "16px",
      overflow:     "hidden",
      boxShadow:    failed ? "0 0 0 1px #dc262620" : "0 1px 3px rgba(0,0,0,.06)",
    }}>

      {/* Header — always visible */}
      <div
        onClick={() => setExpanded(e => !e)}
        style={{
          padding:    "14px 18px",
          display:    "flex",
          alignItems: "center",
          gap:        "12px",
          cursor:     "pointer",
          flexWrap:   "wrap",
        }}>

        {/* Expand toggle */}
        <span style={{ fontSize:"12px", color:"#9ca3af", userSelect:"none" }}>
          {expanded ? "▾" : "▸"}
        </span>

        {/* Job ID */}
        <code style={{
          fontSize:   "11px",
          color:      "#6b7280",
          fontFamily: "monospace",
          flexShrink: 0,
        }} title={job.id}>
          {job.id.substring(0, 12)}…
        </code>

        {/* Status badge */}
        <span style={{
          padding:      "3px 10px",
          borderRadius: "12px",
          fontSize:     "11px",
          fontWeight:   600,
          background:   sc + "18",
          color:        sc,
          border:       `1px solid ${sc}40`,
          flexShrink:   0,
        }}>
          {isReview ? "needs review" : job.status}
        </span>

        {/* Progress bar */}
        <div style={{ flex:1, minWidth:"80px", height:"4px",
          background: dark ? "#1e293b" : "#f1f5f9", borderRadius:"2px" }}>
          <div style={{
            height:       "100%",
            borderRadius: "2px",
            width:        `${Math.round((Math.min(currentStep, 8) / 8) * 100)}%`,
            background:   failed ? "#dc2626" : currentStep >= 8 ? "#16a34a" : "#3b82f6",
            transition:   "width .3s",
          }} />
        </div>
        <span style={{ fontSize:"11px", color:"#9ca3af", flexShrink:0 }}>
          {failed ? `failed at step ${currentStep + 1}` : currentStep >= 8 ? "complete" : `step ${currentStep + 1}/${PIPELINE_STEPS.length}`}
        </span>

        {/* Timestamps */}
        <span style={{ fontSize:"11px", color:"#9ca3af", marginLeft:"auto", flexShrink:0 }}>
          created {fmt(job.created_at)}
          {job.started_at && ` · started ${fmt(job.started_at)}`}
          {job.completed_at && ` · done in ${duration(job.started_at, job.completed_at)}`}
        </span>
      </div>

      {/* Error banner — always visible if failed */}
      {failed && job.error && (
        <div style={{
          margin:       "0 18px 12px",
          padding:      "10px 14px",
          background:   "#fef2f2",
          border:       "1px solid #fecaca",
          borderRadius: "8px",
          fontFamily:   "monospace",
          fontSize:     "12px",
          color:        "#dc2626",
          lineHeight:   1.6,
          whiteSpace:   "pre-wrap",
          wordBreak:    "break-all",
        }}>
          <strong>Error:</strong> {job.error}
        </div>
      )}

      {/* Expanded pipeline steps */}
      {expanded && (
        <div style={{ padding:"4px 18px 18px 18px" }}>
          <div style={{
            fontSize:     "11px",
            color:        "#9ca3af",
            marginBottom: "12px",
            paddingBottom:"10px",
            borderBottom: `1px solid ${dark ? "#1e293b" : "#f1f5f9"}`,
          }}>
            Pipeline trace — based on ARCHITECTURE.md Workflow B.
            Hover any step for details.
          </div>
          {PIPELINE_STEPS.map((step, i) => (
            <StepNode
              key={step.id}
              step={step}
              index={i}
              currentStep={currentStep}
              failed={failed}
              dark={dark}
            />
          ))}
        </div>
      )}
    </div>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

export default function PipelineMonitorPage() {
  const { user }         = useAuth()
  const { dark }         = useContext(ThemeContext)
  const [filter, setFilter] = useState("all")
  const [autoRefresh, setAuto] = useState(true)

  const { data: jobs, isLoading, isFetching, dataUpdatedAt } = useQuery({
    queryKey:        ["pipeline-jobs"],
    queryFn:         () => getAllJobs(),
    refetchInterval: autoRefresh ? 5_000 : false,
    enabled:         !!user,
  })

  const counts = (jobs || []).reduce((acc: Record<string, number>, j: any) => {
    acc[j.status] = (acc[j.status] || 0) + 1
    return acc
  }, {})

  const filtered = (jobs || [])
    .filter((j: any) => filter === "all" || j.status === filter)
    .sort((a: any, b: any) =>
      new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
    )

  const c = {
    bg:      dark ? "#060912" : "#f1f5f9",
    surface: dark ? "#0f172a" : "#ffffff",
    text:    dark ? "#e2e8f0" : "#1e293b",
    muted:   dark ? "#94a3b8" : "#6b7280",
    border:  dark ? "#1e293b" : "#e5e7eb",
  }

  return (
    <div style={{ maxWidth:"860px", margin:"0 auto", padding:"2rem",
      color: c.text, fontFamily:"'Sora', sans-serif" }}>

      {/* Header */}
      <div style={{ marginBottom:"1.5rem" }}>
        <div style={{ display:"flex", alignItems:"center",
          justifyContent:"space-between", flexWrap:"wrap", gap:"12px" }}>
          <div>
            <h1 style={{ margin:0, fontSize:"20px", fontWeight:700 }}>
              Pipeline Monitor
            </h1>
            <p style={{ margin:"4px 0 0", fontSize:"12px", color: c.muted }}>
              Each card shows the full Workflow B trace from ARCHITECTURE.md ·
              click any card to expand · hover steps for details
            </p>
          </div>
          <div style={{ display:"flex", alignItems:"center", gap:"12px" }}>
            <span style={{ fontSize:"12px", color: c.muted }}>
              {isFetching ? "⟳ refreshing..." : dataUpdatedAt ? `updated ${new Date(dataUpdatedAt).toLocaleTimeString()}` : ""}
            </span>
            <label style={{ display:"flex", alignItems:"center",
              gap:"6px", fontSize:"12px", color: c.muted, cursor:"pointer" }}>
              <input type="checkbox" checked={autoRefresh}
                onChange={e => setAuto(e.target.checked)} />
              Auto (5s)
            </label>
          </div>
        </div>
      </div>

      {/* Summary pills */}
      <div style={{ display:"flex", gap:"10px", marginBottom:"1.5rem", flexWrap:"wrap" }}>
        {[
          { key:"all",        label:"All",        val:(jobs||[]).length, color:"#374151" },
          { key:"processing", label:"Processing", val:counts.processing||0, color:"#3b82f6" },
          { key:"pending",    label:"Pending",    val:counts.pending||0,    color:"#f59e0b" },
          { key:"failed",     label:"Failed",     val:counts.failed||0,     color:"#dc2626" },
          { key:"complete",   label:"Complete",   val:counts.complete||0,   color:"#16a34a" },
        ].map(p => (
          <button key={p.key} onClick={() => setFilter(p.key)}
            style={{
              padding:      "5px 14px",
              borderRadius: "20px",
              border:       `1px solid ${filter === p.key ? p.color : c.border}`,
              background:   filter === p.key ? p.color + "15" : c.surface,
              color:        filter === p.key ? p.color : c.muted,
              cursor:       "pointer",
              fontSize:     "12px",
              fontWeight:   filter === p.key ? 600 : 400,
              transition:   "all .15s",
            }}>
            {p.label} ({p.val})
          </button>
        ))}
      </div>

      {/* Job cards */}
      {isLoading ? (
        <div style={{ padding:"3rem", textAlign:"center", color: c.muted }}>
          Loading pipeline jobs...
        </div>
      ) : filtered.length === 0 ? (
        <div style={{ padding:"3rem", textAlign:"center", color: c.muted,
          background: c.surface, borderRadius:"12px", border:`1px solid ${c.border}` }}>
          {filter === "all"
            ? "No jobs yet — upload a document to start the pipeline."
            : `No ${filter} jobs.`}
        </div>
      ) : (
        filtered.map((job: any) => (
          <JobCard key={job.id} job={job} dark={dark} />
        ))
      )}

      {/* Footer */}
      <p style={{ marginTop:"2rem", fontSize:"11px",
        color: c.muted, textAlign:"center" }}>
        Internal debug page · not linked from nav · requires login ·
        access at <code>/pipeline</code>
      </p>
    </div>
  )
}
