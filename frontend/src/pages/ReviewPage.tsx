// frontend/src/pages/ReviewPage.tsx
import { useState } from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { useParams, useNavigate } from "react-router-dom"
import { getExtraction, submitCorrection, getSessionByExtraction, getBatchStatus, markExtractionComplete } from "../lib/api"

// ── Sub-components ────────────────────────────────────────────────────────────

function FieldCard({
  field, isSelected, isSubmitted,
  onClick,
}: {
  field: any
  isSelected: boolean
  isSubmitted: boolean
  onClick: () => void
}) {
  return (
    <div
      onClick={onClick}
      style={{
        padding:"10px 12px", marginBottom:"6px", borderRadius:"6px",
        cursor:"pointer", border:"1px solid",
        borderColor: isSelected ? "#3C3489" : "#e5e7eb",
        background: isSubmitted ? "#f0fdf4" : isSelected ? "#f5f3ff" : "#fff",
      }}>
      <div style={{ display:"flex", justifyContent:"space-between", alignItems:"flex-start" }}>
        <div>
          <div style={{ fontSize:"13px", fontWeight:500 }}>{field.field_name}</div>
          <div style={{ fontSize:"12px", color:"#374151", marginTop:"2px", wordBreak:"break-all" }}>
            {field.value || <em style={{ color:"#9ca3af" }}>not found</em>}
          </div>
        </div>
        <div style={{ textAlign:"right", flexShrink:0, marginLeft:"8px" }}>
          <div style={{ fontSize:"10px", color:"#9ca3af" }}>
            {field.decision?.replace("_", " ")}
          </div>
        </div>
      </div>
      {isSubmitted && (
        <div style={{ fontSize:"11px", color:"#16a34a", marginTop:"4px" }}>✓ Corrected</div>
      )}
    </div>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

const STATUS_COLORS: Record<string, string> = {
  pending:    "#94a3b8",
  processing: "#3b82f6",
  complete:   "#16a34a",
  failed:     "#dc2626",
}

export default function ReviewPage() {
  const { id }       = useParams<{ id: string }>()
  const qc           = useQueryClient()
  const navigate     = useNavigate()

  const [selected,    setSelected]    = useState<string | null>(null)
  const [correction,  setCorrection]  = useState("")
  const [submitted,   setSubmitted]   = useState<Set<string>>(new Set())

  // Poll every 2 s until extraction is complete
  const { data, isLoading } = useQuery({
    queryKey: ["extraction", id],
    queryFn:  () => getExtraction(id!),
    refetchInterval: (query) =>
      query.state.data?.status === "complete" || query.state.data?.status === "failed"
        ? false : 2000,
  })

  const correctMutation = useMutation({
    mutationFn: (payload: any) => submitCorrection(payload),
    onSuccess: async (_res, vars: any) => {
      const nextSubmitted = new Set(submitted)
      nextSubmitted.add(vars.field_name)
      setSubmitted(nextSubmitted)

      qc.invalidateQueries({ queryKey: ["extraction", id] })
      setSelected(null)

      const reviewable = (data?.fields || []).filter(
        (f: any) => f.decision === "human_review" || f.decision === "fail"
      )
      const allFieldsDone = reviewable.length > 0 && nextSubmitted.size >= reviewable.length

      if (allFieldsDone) {
        // Mark this PDF/extraction as complete
        try { await markExtractionComplete(id!) } catch {}

        // Then check if the whole session is finished
        try {
          const { session_id } = await getSessionByExtraction(id!)
          const batch = await getBatchStatus(session_id)
          if (batch.total > 0 && (batch.completed + batch.failed) >= batch.total) {
            navigate(`/upload?session_id=${session_id}`)
            return
          }
        } catch {}
        navigate("/queue")
      }
    },
  })

  const selectField = (fieldName: string, value: string) => {
    setSelected(fieldName)
    setCorrection(value || "")
  }

  if (isLoading) return <div style={{ padding:"2rem" }}>Loading...</div>

  const fields        = data?.fields || []
  const selectedField = fields.find((f: any) => f.field_name === selected)

  return (
    <div style={{ display:"flex", height:"100vh", fontFamily:"system-ui" }}>

      {/* ── Left panel: field list ── */}
      <div style={{ width:"360px", borderRight:"1px solid #e5e7eb", overflowY:"auto", padding:"1rem" }}>
        <div style={{ marginBottom:"1rem" }}>
          <h2 style={{ fontSize:"16px", marginBottom:"4px" }}>Extraction Results</h2>
          <div style={{ display:"flex", alignItems:"center", gap:"6px" }}>
            <div style={{ width:"8px", height:"8px", borderRadius:"50%",
              background: STATUS_COLORS[data?.status] || "#94a3b8" }} />
            <span style={{ fontSize:"12px", color:"#666", textTransform:"capitalize" }}>
              {data?.status}
            </span>
          </div>
        </div>

        {data?.status !== "complete" && (
          <div style={{ padding:"12px", background:"#eff6ff", borderRadius:"6px",
            marginBottom:"1rem", fontSize:"13px" }}>
            ⏳ Processing your document...
          </div>
        )}

        {fields.map((f: any) => (
          <FieldCard
            key={f.field_name}
            field={f}
            isSelected={selected === f.field_name}
            isSubmitted={submitted.has(f.field_name)}
            onClick={() => selectField(f.field_name, f.value)}
          />
        ))}
      </div>

      {/* ── Right panel: correction form ── */}
      <div style={{ flex:1, padding:"1.5rem", overflowY:"auto" }}>
        {selected && selectedField ? (
          <div style={{ maxWidth:"600px" }}>
            <h2 style={{ fontSize:"16px", marginBottom:"1.5rem" }}>
              Correct: <span style={{ color:"#3C3489" }}>{selected}</span>
            </h2>

            {/* Correction input */}
            <div style={{ marginBottom:"1rem" }}>
              <label style={{ display:"block", fontSize:"13px", fontWeight:500, marginBottom:"4px" }}>
                Corrected value
              </label>
              <input
                value={correction}
                onChange={e => setCorrection(e.target.value)}
                style={{ width:"100%", padding:"8px 10px", border:"1px solid #d1d5db",
                  borderRadius:"6px", fontSize:"14px" }}
                placeholder="Enter the correct value"
              />
            </div>

            <button
              onClick={() => correctMutation.mutate({
                extraction_id:   id!,
                field_name:      selected,
                original_value:  selectedField.value || "",
                corrected_value: correction,
                failure_type:    "none",
                prompt_id:       selectedField.prompt_id,
                retrieved_chunk_ids: selectedField.retrieved_chunks,
                confidence_score:    selectedField.confidence_score,
                context_relevance:   selectedField.context_relevance,
                answer_relevance:    selectedField.answer_relevance,
                groundedness:        selectedField.groundedness,
              })}
              disabled={!correction.trim() || correctMutation.isPending}
              style={{ padding:"10px 24px", background:"#3b82f6", color:"#fff",
                border:"none", borderRadius:"6px", cursor:"pointer", fontSize:"14px",
                opacity: !correction.trim() ? 0.5 : 1 }}>
              {correctMutation.isPending ? "Saving..." : "Submit Correction"}
            </button>

            {correctMutation.isSuccess && (
              <div style={{ marginTop:"8px", fontSize:"13px", color:"#16a34a" }}>
                ✓ Correction saved.
              </div>
            )}
          </div>
        ) : (
          <div style={{ color:"#9ca3af", fontSize:"14px", paddingTop:"3rem" }}>
            ← Select a field to review and correct
          </div>
        )}
      </div>
    </div>
  )
}
