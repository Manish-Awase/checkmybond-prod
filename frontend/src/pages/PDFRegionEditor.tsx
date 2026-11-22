// frontend/src/pages/PDFRegionEditor.tsx
import { useState, useRef, useEffect, useCallback } from "react"
declare const pdfjsLib: any

export interface Region {
  id:               string
  field_name:       string
  page:             number
  x:                number
  y:                number
  w:                number
  h:                number
  correction_value?: string
  fixed_text_size?: boolean
}

interface Props {
  pdfFile:          File
  onSubmit:         (regions: Region[]) => void
  onCancel:         () => void
  dark:             boolean
  showCorrections?: boolean   // true = type correction value (single PDF)
  autoConfirmFields?: boolean  // true = auto-name + click ✓ (folder + checkbox)
}

const COLOURS = [
  "#3b82f6", "#16a34a", "#dc2626", "#f59e0b",
  "#8b5cf6", "#ec4899", "#0891b2", "#65a30d",
]
function colourFor(idx: number) { return COLOURS[idx % COLOURS.length] }

export default function PDFRegionEditor({
  pdfFile, onSubmit, onCancel, dark,
  showCorrections = false,
  autoConfirmFields = false,
}: Props) {
  const canvasRef    = useRef<HTMLCanvasElement>(null)
  const overlayRef   = useRef<HTMLCanvasElement>(null)

  const [pdf,         setPdf]         = useState<any>(null)
  const [page,        setPage]        = useState(1)
  const [totalPages,  setTotalPages]  = useState(1)
  const [scale,       setScale]       = useState(1.5)
  const [regions,     setRegions]     = useState<Region[]>([])
  const [editingId,   setEditingId]   = useState<string | null>(null)
  const [labelInput,  setLabelInput]  = useState("")
  const [fixedTextSize, setFixedTextSize] = useState(false)
  const [viewport,    setViewport]    = useState<any>(null)
  const [loading,     setLoading]     = useState(true)
  const [error,       setError]       = useState("")

  /* ── Refs for lag-free canvas (no React re-renders during drag) ── */
  const regionsRef    = useRef<Region[]>([])
  const viewportRef   = useRef<any>(null)
  const scaleRef      = useRef(scale)
  const pageRef       = useRef(page)
  const drawingRef    = useRef(false)
  const startPtRef    = useRef({ x: 0, y: 0 })
  const dragRectRef   = useRef<{x:number,y:number,w:number,h:number} | null>(null)
  const rafRef        = useRef<number>(0)

  useEffect(() => { regionsRef.current = regions }, [regions])
  useEffect(() => { scaleRef.current = scale }, [scale])
  useEffect(() => { pageRef.current = page }, [page])

  /* ---------- Escape to close ---------- */
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onCancel() }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [onCancel])

  /* ---------- PDF.js bootstrap ---------- */
  useEffect(() => {
    if (typeof pdfjsLib !== "undefined") return
    const script = document.createElement("script")
    script.src = "https://cdnjs.cloudflare.com/ajax/libs/pdf.js/3.11.174/pdf.min.js"
    script.onload = () => {
      (window as any).pdfjsLib.GlobalWorkerOptions.workerSrc =
        "https://cdnjs.cloudflare.com/ajax/libs/pdf.js/3.11.174/pdf.worker.min.js"
    }
    document.head.appendChild(script)
  }, [])

  /* ---------- Load PDF ---------- */
  useEffect(() => {
    if (!pdfFile) return
    setLoading(true)
    setError("")
    const reader = new FileReader()
    reader.onload = async (e) => {
      try {
        let attempts = 0
        while (typeof (window as any).pdfjsLib === "undefined" && attempts < 30) {
          await new Promise(r => setTimeout(r, 200))
          attempts++
        }
        if (typeof (window as any).pdfjsLib === "undefined") {
          setError("PDF.js failed to load — check internet connection")
          setLoading(false)
          return
        }
        const data    = new Uint8Array(e.target!.result as ArrayBuffer)
        const pdfDoc  = await (window as any).pdfjsLib.getDocument({ data }).promise
        setPdf(pdfDoc)
        setTotalPages(pdfDoc.numPages)
        setPage(1)
        setLoading(false)
      } catch (err: any) {
        setError(`Failed to load PDF: ${err.message}`)
        setLoading(false)
      }
    }
    reader.readAsArrayBuffer(pdfFile)
  }, [pdfFile])

  /* ---------- Overlay drawing (reads refs → never stale) ---------- */
  const drawOverlay = useCallback(() => {
    const overlay = overlayRef.current
    const vp = viewportRef.current
    if (!overlay || !vp) return
    const ctx = overlay.getContext("2d")!
    ctx.clearRect(0, 0, overlay.width, overlay.height)

    const s = scaleRef.current
    const currentPage = pageRef.current

    regionsRef.current
      .filter(r => r.page === currentPage)
      .forEach((r, i) => {
        const sx = r.x * s
        const sy = r.y * s
        const sw = r.w * s
        const sh = r.h * s
        const col = colourFor(i)
        ctx.strokeStyle = col
        ctx.lineWidth = 2
        ctx.strokeRect(sx, sy, sw, sh)
        ctx.fillStyle = col + "20"
        ctx.fillRect(sx, sy, sw, sh)
        ctx.fillStyle = col
        ctx.fillRect(sx, sy - 20, Math.min(sw, 200), 20)
        ctx.fillStyle = "#ffffff"
        ctx.font = "bold 11px sans-serif"
        ctx.fillText(r.field_name || "unnamed", sx + 4, sy - 6)
      })

    const drag = dragRectRef.current
    if (drag) {
      ctx.strokeStyle = "#f59e0b"
      ctx.lineWidth = 2
      ctx.setLineDash([6, 3])
      ctx.strokeRect(drag.x, drag.y, drag.w, drag.h)
      ctx.fillStyle = "#f59e0b20"
      ctx.fillRect(drag.x, drag.y, drag.w, drag.h)
      ctx.setLineDash([])
    }
  }, [])

  const scheduleDraw = useCallback(() => {
    cancelAnimationFrame(rafRef.current)
    rafRef.current = requestAnimationFrame(drawOverlay)
  }, [drawOverlay])

  /* ---------- Render page ---------- */
  useEffect(() => {
    if (!pdf || !canvasRef.current) return
    let cancelled = false
    ;(async () => {
      const pdfPage = await pdf.getPage(page)
      const vp = pdfPage.getViewport({ scale })
      viewportRef.current = vp
      setViewport(vp)

      const canvas = canvasRef.current!
      canvas.width = vp.width
      canvas.height = vp.height
      const overlay = overlayRef.current!
      overlay.width = vp.width
      overlay.height = vp.height

      const ctx = canvas.getContext("2d")!
      await pdfPage.render({ canvasContext: ctx, viewport: vp }).promise
      if (!cancelled) drawOverlay()
    })()
    return () => { cancelled = true }
  }, [pdf, page, scale, drawOverlay])

  function getCanvasPos(e: React.MouseEvent<HTMLCanvasElement>): { x: number; y: number } {
    const rect = overlayRef.current!.getBoundingClientRect()
    const vp = viewportRef.current!
    return {
      x: (e.clientX - rect.left) * (vp.width / rect.width),
      y: (e.clientY - rect.top) * (vp.height / rect.height),
    }
  }

  /* ---------- Mouse handlers (zero state updates while dragging) ---------- */
  const onMouseDown = (e: React.MouseEvent<HTMLCanvasElement>) => {
    if (e.button !== 0) return
    const pos = getCanvasPos(e)
    drawingRef.current = true
    startPtRef.current = pos
    dragRectRef.current = { x: pos.x, y: pos.y, w: 0, h: 0 }
    scheduleDraw()
  }

  const onMouseMove = (e: React.MouseEvent<HTMLCanvasElement>) => {
    if (!drawingRef.current) return
    const pos = getCanvasPos(e)
    const start = startPtRef.current
    dragRectRef.current = {
      x: Math.min(pos.x, start.x),
      y: Math.min(pos.y, start.y),
      w: Math.abs(pos.x - start.x),
      h: Math.abs(pos.y - start.y),
    }
    scheduleDraw()
  }

  const onMouseUp = (e: React.MouseEvent<HTMLCanvasElement>) => {
    if (!drawingRef.current) return
    drawingRef.current = false
    const drag = dragRectRef.current
    dragRectRef.current = null
    scheduleDraw()

    if (!drag || drag.w < 10 || drag.h < 10) return

    const s = scaleRef.current
    const px = drag.x / s
    const py = drag.y / s
    const pw = drag.w / s
    const ph = drag.h / s

    const nextFieldNum = Math.max(0, ...regionsRef.current.map(r => {
      const m = r.field_name.match(/^field(\d+)$/)
      return m ? parseInt(m[1], 10) : 0
    })) + 1

    const newRegion: Region = {
      id: crypto.randomUUID(),
      field_name: (showCorrections || autoConfirmFields) ? `field${nextFieldNum}` : "",
      page: pageRef.current,
      x: Math.round(px),
      y: Math.round(py),
      w: Math.round(pw),
      h: Math.round(ph),
      correction_value: showCorrections ? "" : undefined,
    }

    const nextRegions = [...regionsRef.current, newRegion]
    regionsRef.current = nextRegions
    setRegions(nextRegions)
    setEditingId(newRegion.id)
    if (!autoConfirmFields) setLabelInput("")
  }

  const deleteRegion = (id: string) => {
    const next = regionsRef.current.filter(r => r.id !== id)
    regionsRef.current = next
    setRegions(next)
    if (editingId === id) setEditingId(null)
    scheduleDraw()
  }

  const updateLabel = (id: string, value: string) => {
    const next = regionsRef.current.map(r => {
      if (r.id !== id) return r
      if (showCorrections) return { ...r, correction_value: value }
      return { ...r, field_name: value }
    })
    regionsRef.current = next
    setRegions(next)
  }

  const canSubmit = regions.length > 0 && editingId === null &&
    (autoConfirmFields || regions.every(r => showCorrections
      ? (r.correction_value?.trim().length ?? 0) > 0
      : r.field_name.trim().length > 0))

  const c = {
    bg:     dark ? "#0f172a" : "#f8fafc",
    card:   dark ? "#1e293b" : "#ffffff",
    border: dark ? "#334155" : "#e2e8f0",
    text:   dark ? "#e2e8f0" : "#1e293b",
    muted:  dark ? "#94a3b8" : "#6b7280",
  }

  const baseBtn: React.CSSProperties = {
    padding:      "6px 12px",
    borderRadius: "6px",
    border:       "none",
    cursor:       "pointer",
    fontSize:     "12px",
    fontWeight:   600,
    background:   "rgba(148,163,184,.2)",
    color:        c.text,
    transition:   "all .15s",
  }

  const inputPlaceholder = showCorrections
    ? "Enter correction text"
    : "Field name e.g. Invoice No"

  return (
    <div style={{ display:"flex", flexDirection:"column", height:"100%",
      background: c.bg, color: c.text }}>
      <div style={{ display:"flex", alignItems:"center", gap:"12px", padding:"12px 16px",
        background: c.card, borderBottom:`1px solid ${c.border}`, flexWrap:"wrap" }}>
        <span style={{ fontWeight:700, fontSize:"14px" }}>📐 Field Region Editor</span>
        <span style={{ fontSize:"12px", color: c.muted }}>
          Draw rectangles to mark fields · right-click a rectangle to delete
        </span>
        <div style={{ display:"flex", alignItems:"center", gap:"8px", marginLeft:"auto" }}>
          <button onClick={() => setPage(p => Math.max(1, p - 1))} disabled={page === 1}
            style={{ ...baseBtn, opacity: page === 1 ? 0.4 : 1 }}>‹ Prev</button>
          <span style={{ fontSize:"12px", color: c.muted, minWidth:"80px", textAlign:"center" }}>
            Page {page} / {totalPages}
          </span>
          <button onClick={() => setPage(p => Math.min(totalPages, p + 1))} disabled={page === totalPages}
            style={{ ...baseBtn, opacity: page === totalPages ? 0.4 : 1 }}>Next ›</button>
        </div>
        <div style={{ display:"flex", alignItems:"center", gap:"4px" }}>
          <button onClick={() => setScale(s => Math.max(0.5, s - 0.25))} style={baseBtn}>−</button>
          <span style={{ fontSize:"12px", color: c.muted, minWidth:"40px", textAlign:"center" }}>
            {Math.round(scale * 100)}%
          </span>
          <button onClick={() => setScale(s => Math.min(3, s + 0.25))} style={baseBtn}>+</button>
        </div>
        <label style={{ display: "flex", alignItems: "center", gap: "6px", fontSize: "12px", color: c.text, cursor: "pointer", userSelect: "none" }}>
          <input
            type="checkbox"
            checked={fixedTextSize}
            onChange={e => setFixedTextSize(e.target.checked)}
            style={{ cursor: "pointer" }}
          />
          Fixed text size
        </label>
        <button onClick={onCancel}
          style={{ ...baseBtn, background:"transparent", border:`1px solid ${c.border}`, color: c.muted }}>
          Cancel
        </button>
        <button onClick={() => onSubmit(regions.map(r => ({ ...r, fixed_text_size: fixedTextSize })))} disabled={!canSubmit}
          style={{ ...baseBtn,
            background: canSubmit ? "#3b82f6" : "#cbd5e1",
            color: "#fff",
            opacity: canSubmit ? 1 : 0.6,
            cursor: canSubmit ? "pointer" : "not-allowed",
          }}>
          Submit {regions.length > 0 ? `(${regions.length} fields)` : ""}
        </button>
      </div>

      <div style={{ display:"flex", flex:1, overflow:"hidden" }}>
        <div style={{ flex:1, overflow:"auto", position:"relative", cursor:"crosshair" }}>
          {loading && (
            <div style={{ display:"flex", alignItems:"center", justifyContent:"center",
              height:"100%", color: c.muted, fontSize:"14px" }}>Loading PDF...</div>
          )}
          {error && (
            <div style={{ display:"flex", alignItems:"center", justifyContent:"center",
              height:"100%", color:"#dc2626", fontSize:"14px" }}>{error}</div>
          )}
          {!loading && !error && (
            <div style={{ position:"relative", display:"inline-block", margin:"16px" }}>
              <canvas ref={canvasRef} style={{ display:"block", boxShadow:"0 2px 12px rgba(0,0,0,.2)" }} />
              <canvas
                ref={overlayRef}
                style={{ position:"absolute", top:0, left:0, cursor:"crosshair" }}
                onMouseDown={onMouseDown}
                onMouseMove={onMouseMove}
                onMouseUp={onMouseUp}
                onContextMenu={e => {
                  e.preventDefault()
                  const pos = getCanvasPos(e)
                  const s = scaleRef.current
                  const px = pos.x / s
                  const py = pos.y / s
                  const hit = regionsRef.current
                    .filter(r => r.page === pageRef.current)
                    .find(r => px >= r.x && px <= r.x + r.w && py >= r.y && py <= r.y + r.h)
                  if (hit) deleteRegion(hit.id)
                }}
              />
            </div>
          )}
        </div>

        <div style={{ width:"280px", borderLeft:`1px solid ${c.border}`,
          background: c.card, display:"flex", flexDirection:"column", overflow:"hidden" }}>
          <div style={{ padding:"12px 14px", borderBottom:`1px solid ${c.border}`,
            fontSize:"12px", fontWeight:700, textTransform:"uppercase",
            letterSpacing:".07em", color: c.muted }}>
            Fields ({regions.length})
          </div>
          <div style={{ flex:1, overflowY:"auto", padding:"8px" }}>
            {regions.length === 0 && (
              <div style={{ padding:"24px 12px", textAlign:"center",
                color: c.muted, fontSize:"12px", lineHeight:1.6 }}>
                Draw a rectangle on the PDF to define a field region
              </div>
            )}
            {regions.map((r, i) => {
              const col       = colourFor(i)
              const isEditing = editingId === r.id
              return (
                <div key={r.id} style={{ marginBottom:"10px", padding:"10px",
                  borderRadius:"8px", border:`1px solid ${isEditing ? col : c.border}`,
                  background: isEditing ? col + "10" : "transparent" }}>
                  <div style={{ display:"flex", alignItems:"center", gap:"8px",
                    marginBottom:"6px" }}>
                    <div style={{ width:"10px", height:"10px", borderRadius:"50%",
                      background: col, flexShrink:0 }} />
                    <span style={{ fontSize:"10px", color: c.muted, fontFamily:"monospace" }}>
                      {r.field_name} · pg {r.page} · {r.w}×{r.h}px @ ({r.x},{r.y})
                    </span>
                    <button onClick={() => deleteRegion(r.id)}
                      style={{ marginLeft:"auto", background:"none", border:"none",
                        cursor:"pointer", color:"#dc2626", fontSize:"14px", lineHeight:1,
                        padding:"0 2px" }} title="Delete region">×</button>
                  </div>

                  {isEditing ? (
                    <div style={{ display:"flex", gap:"4px" }}>
                      {autoConfirmFields ? (
                        <>
                          <span style={{ flex:1, padding:"5px 8px", fontSize:"12px",
                            color: c.text, fontWeight: 500, display:"flex", alignItems:"center" }}>
                            {r.field_name}
                          </span>
                          <button
                            onClick={() => setEditingId(null)}
                            style={{ ...baseBtn, background: col, color:"#fff",
                              padding:"5px 8px", fontSize:"12px" }}>✓</button>
                        </>
                      ) : (
                        <>
                          <input
                            autoFocus
                            value={labelInput}
                            onChange={e => setLabelInput(e.target.value)}
                            onKeyDown={e => {
                              if (e.key === "Enter") {
                                if (labelInput.trim()) {
                                  updateLabel(r.id, labelInput.trim())
                                  setEditingId(null)
                                }
                              }
                              if (e.key === "Escape") setEditingId(null)
                            }}
                            placeholder={inputPlaceholder}
                            style={{ flex:1, padding:"5px 8px", fontSize:"12px",
                              borderRadius:"6px", border:`1px solid ${col}`,
                              outline:"none", background: dark ? "#0f172a" : "#fff",
                              color: c.text }}
                          />
                          <button
                            onClick={() => {
                              if (labelInput.trim()) {
                                updateLabel(r.id, labelInput.trim())
                                setEditingId(null)
                              }
                            }}
                            style={{ ...baseBtn, background: col, color:"#fff",
                              padding:"5px 8px", fontSize:"12px" }}>✓</button>
                        </>
                      )}
                    </div>
                  ) : (
                    <div
                      onClick={() => {
                        if (autoConfirmFields) return
                        setEditingId(r.id)
                        setLabelInput(showCorrections ? (r.correction_value || "") : r.field_name)
                      }}
                      style={{ padding:"5px 8px", borderRadius:"6px",
                        cursor: autoConfirmFields ? "default" : "text",
                        border:`1px solid ${c.border}`, fontSize:"12px",
                        color: (showCorrections ? r.correction_value : r.field_name) ? c.text : c.muted,
                        background: dark ? "#0f172a" : "#f8fafc",
                        minHeight:"28px" }}>
                      {showCorrections
                        ? (r.correction_value || <em style={{ color: c.muted }}>click to enter correction</em>)
                        : (r.field_name || <em style={{ color: c.muted }}>click to name field</em>)}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
          {regions.length > 0 && !canSubmit && (
            <div style={{ padding:"10px 14px", borderTop:`1px solid ${c.border}`,
              fontSize:"11px", color:"#f59e0b" }}>
              ⚠ {autoConfirmFields
                ? "Confirm all fields with ✓ before submitting"
                : showCorrections
                  ? "Enter correction text for all fields"
                  : "Name all fields before submitting"}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

