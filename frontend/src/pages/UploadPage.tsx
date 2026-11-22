// frontend/src/pages/UploadPage.tsx
import { useState, useEffect, useContext, useRef } from "react"
import { useMutation }                        from "@tanstack/react-query"
import { useNavigate, useSearchParams }                        from "react-router-dom"
import {
  downloadCorrectedPDFs,
  clearSession,
  getTokenBalance,
  getDocumentConfig,
  getExtractionStatus,
  getBatchStatus,
  uploadOCR,
} from "../lib/api"
import { ThemeContext }                       from "../App"
import PDFRegionEditor, { Region }            from "./PDFRegionEditor"
import LlmSchemaUpload                        from "../components/LlmSchemaUpload"

const SESSION_KEY = "hitl_ocr_session"
const REGIONS_KEY = "hitl_ocr_regions"

// Supported file extensions
const SUPPORTED_EXTS = [".pdf", ".docx", ".doc", ".png", ".jpg", ".jpeg", ".tiff", ".tif", ".bmp", ".gif", ".webp"]

function getFileExt(name: string): string {
  return name.slice(name.lastIndexOf(".")).toLowerCase()
}

function isSupportedFile(file: File): boolean {
  return SUPPORTED_EXTS.includes(getFileExt(file.name))
}

function fileTypeLabel(file: File): string {
  const ext = getFileExt(file.name)
  if (ext === ".pdf") return "PDF"
  if ([".docx", ".doc"].includes(ext)) return "Word"
  if ([".png", ".jpg", ".jpeg", ".tiff", ".tif", ".bmp", ".gif", ".webp"].includes(ext)) return "Image"
  return "File"
}

export default function UploadPage() {
  const { dark } = useContext(ThemeContext)
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()

  // ── Redirected from ReviewPage with ?session_id=xxx ───────────────────
  useEffect(() => {
    const urlSessionId = searchParams.get("session_id")
    if (!urlSessionId) return
    saveSessionId(urlSessionId)
    let interval: ReturnType<typeof setInterval> | null = null
    const poll = async () => {
      try {
        const res = await getBatchStatus(urlSessionId)
        if (res.total > 0) {
          setSession({
            id: urlSessionId,
            total: res.total,
            queued: res.completed,
            skipped: 0,
            failed: res.failed,
            remaining: res.total - (res.completed + res.failed),
            percent: res.percent,
            status: res.percent >= 100 ? "complete" : "in_progress",
          })
          if (res.percent >= 100 || (res.completed + res.failed) >= res.total) {
            if (interval) clearInterval(interval)
            setSearchParams({}, { replace: true })
          }
        }
      } catch {}
    }
    poll()
    interval = setInterval(poll, 2000)
    return () => { if (interval) clearInterval(interval) }
  }, [searchParams])

  // Token balance
  const [tokenBalance, setTokenBalance] = useState<number | null>(null)
  const [insufficientTokens, setInsufficientTokens] = useState(false)
  const refreshBalance = () => {
    getTokenBalance()
      .then(d => setTokenBalance(d.token_balance))
      .catch(() => {})
  }
  useEffect(() => { refreshBalance() }, [])

  // LLM vs OCR mode
  const [useLlm, setUseLlm] = useState<boolean | null>(null)
  useEffect(() => {
    getDocumentConfig()
      .then(d => setUseLlm(d.use_llm))
      .catch(() => setUseLlm(false))
  }, [])

  // Step 1 mutual exclusion
  const [activeSection, setActiveSection] = useState<"file" | "folder" | null>(null)

  // OCR upload state
  const [folderPdfs,  setFolderPdfs]  = useState<File[]>([])
  const [editorOpen,  setEditorOpen]  = useState(false)
  const [editorPdf,   setEditorPdf]  = useState<File | null>(null)
  const [regions,     setRegions]    = useState<Region[]>([])
  const [session,     setSession]    = useState<{
    id: string; total: number; queued: number; skipped: number
    failed: number; remaining: number; percent: number; status: string
  } | null>(null)
  const [uploadProgress, setProgress] = useState<{
    chunkDone: number; chunkTotal: number; chunkIndex: number; totalChunks: number
  } | null>(null)
  const [interruptedMsg, setInterruptedMsg] = useState<string | null>(null)
  const [downloading,    setDownloading]    = useState(false)
  const [downloadError,  setDownloadError]  = useState<string | null>(null)

  // Abort controller for stopping uploads
  const abortRef = useRef<AbortController | null>(null)

  // Direct processing state
  const [directProcessing, setDirectProcessing] = useState(false)
  const [directComplete,   setDirectComplete]   = useState(false)
  const [directFailed,     setDirectFailed]     = useState(false)
  const [directExtractionId, setDirectExtractionId] = useState<string | null>(null)
  const [directSessionId,    setDirectSessionId]    = useState<string | null>(null)

  // Folder "apply corrections to all" checkbox
  const [applyCorrectionsToAll, setApplyCorrectionsToAll] = useState(false)
  const [batchProgress, setBatchProgress] = useState<{
    completed: number; failed: number; total: number; percent: number
  } | null>(null)

  // Session ID ref + reactive flag
  const sessionIdRef = useRef<string | null>(
    (() => { try { return localStorage.getItem(SESSION_KEY) } catch { return null } })()
  )
  const [hasSessionId, setHasSessionId] = useState(() => !!sessionIdRef.current)

  const saveSessionId = (id: string) => {
    sessionIdRef.current = id
    try { localStorage.setItem(SESSION_KEY, id) } catch {}
    setHasSessionId(true)
  }
  const clearSessionId = () => {
    sessionIdRef.current = null
    try { localStorage.removeItem(SESSION_KEY) } catch {}
    setHasSessionId(false)
  }
  const getSessionId = () => sessionIdRef.current

  const MAX_BATCH = 100

  // Restore persisted regions on mount
  useEffect(() => {
    try {
      const saved = localStorage.getItem(REGIONS_KEY)
      if (saved) setRegions(JSON.parse(saved))
    } catch {}
  }, [])

  // Persist regions whenever they change
  useEffect(() => {
    try {
      if (regions.length > 0) {
        localStorage.setItem(REGIONS_KEY, JSON.stringify(regions))
      }
    } catch {}
  }, [regions])

  // Poll extraction status for SINGLE-FILE direct mode only
  useEffect(() => {
    if (!directProcessing || !directExtractionId) return
    let interval = setInterval(async () => {
      try {
        const res = await getExtractionStatus(directExtractionId)
        if (res.status === "complete") {
          setDirectProcessing(false)
          setDirectComplete(true)
          clearInterval(interval)
        } else if (res.status === "failed") {
          setDirectProcessing(false)
          setDirectFailed(true)
          clearInterval(interval)
        }
      } catch {}
    }, 2000)
    return () => clearInterval(interval)
  }, [directProcessing, directExtractionId])

  // Poll batch status for FOLDER direct mode only
  useEffect(() => {
    if (!directProcessing || !directSessionId || directExtractionId) return
    let interval = setInterval(async () => {
      try {
        const res = await getBatchStatus(directSessionId)
        setBatchProgress(res)
        if (res.percent >= 100 || (res.completed + res.failed) >= res.total) {
          setDirectProcessing(false)
          setDirectComplete(true)
          clearInterval(interval)
        }
      } catch {}
    }, 2000)
    return () => clearInterval(interval)
  }, [directProcessing, directSessionId, directExtractionId])

  // OCR batch upload mutation
  const ocrMutation = useMutation({
    mutationFn: async () => {
      setInterruptedMsg(null)
      setDirectProcessing(false)
      setDirectComplete(false)
      setDirectFailed(false)
      setDirectExtractionId(null)
      setDirectSessionId(null)
      setBatchProgress(null)

      const existingId = getSessionId()
      const sessionId  = existingId || crypto.randomUUID()
      saveSessionId(sessionId)

      const chunks: File[][] = []
      for (let i = 0; i < folderPdfs.length; i += MAX_BATCH) {
        chunks.push(folderPdfs.slice(i, i + MAX_BATCH))
      }

      let lastSummary: any = null
      let lastTokenBalance: number | undefined
      let lastInsufficientTokens = false

      for (let c = 0; c < chunks.length; c++) {
        const chunk = chunks[c]
        setProgress({
          chunkDone: 0, chunkTotal: chunk.length,
          chunkIndex: c + 1, totalChunks: chunks.length,
        })

        const form = new FormData()
        form.append("session_id", sessionId)
        form.append("apply_corrections_to_all", activeSection === "folder" && applyCorrectionsToAll ? "true" : "false")
        for (const pdf of chunk) form.append("pdfs[]", pdf)
        const blob = new Blob([JSON.stringify(regions)], { type: "application/json" })
        form.append("regions", blob, "regions.json")

        abortRef.current = new AbortController()
        let res: Response
        try {
          res = await uploadOCR(form, abortRef.current.signal)
        } catch (networkErr) {
          setInterruptedMsg(
            `Network error on chunk ${c + 1}/${chunks.length}. ` +
            `Files in previous chunks are safely queued. ` +
            `Click "Resume Upload" to continue from where it stopped.`
          )
          setProgress(null)
          if (lastSummary) setSession(mapSummary(lastSummary))
          throw new Error("Network error — click Resume to continue")
        }

        if (res.status === 429) {
          const err = await res.json().catch(() => ({}))
          throw new Error(err.detail || "Queue at capacity — try again later")
        }

        const data = await res.json().catch(() => ({}))
        lastSummary = data.summary
        if (typeof data.token_balance === "number") lastTokenBalance = data.token_balance
        if (data.insufficient_tokens) lastInsufficientTokens = true

        if (data.summary) setSession(mapSummary(data.summary))

        if (data.insufficient_tokens) {
          setInterruptedMsg(
            `Ran out of tokens after ${data.summary?.queued ?? "some"} documents ` +
            `(${data.fields_per_doc ?? "?"} tokens/document). ` +
            `Top up on the Profile page, then click "Resume Upload" to continue.`
          )
          setProgress(null)
          if (typeof data.token_balance === "number") setTokenBalance(data.token_balance)
          return { complete: false, summary: data.summary }
        }

        if (res.status === 206 || data.interrupted) {
          setInterruptedMsg(
            `Connection dropped after ${data.summary?.queued ?? "some"} files. ` +
            `Click "Resume Upload" to continue — already-queued files are safe.`
          )
          setProgress(null)
          return data
        }

        setProgress(p => p ? { ...p, chunkDone: chunk.length } : null)

        if (c === chunks.length - 1 && data.direct_process) {
          return {
            complete: true,
            summary: data.summary,
            tokenBalance: lastTokenBalance,
            insufficientTokens: lastInsufficientTokens,
            directProcess: true,
            directExtractionId: data.direct_extraction_id ?? null,
            directSessionId: sessionId,
          }
        }
      }

      setProgress(null)
      if (lastSummary?.remaining === 0) clearSessionId()

      return {
        complete: lastSummary?.remaining === 0,
        summary: lastSummary,
        tokenBalance: lastTokenBalance,
        insufficientTokens: lastInsufficientTokens,
      }
    },
    onSuccess: (data: any) => {
      if (data?.summary) setSession(mapSummary(data.summary))
      if (typeof data?.tokenBalance === "number") setTokenBalance(data.tokenBalance)
      setInsufficientTokens(!!data?.insufficientTokens)

      if (data?.directProcess) {
        setDirectProcessing(true)
        setDirectExtractionId(data.directExtractionId || null)
        setDirectSessionId(data.directSessionId)
        return
      }

      clearSessionId()
      setRegions([])
      try { localStorage.removeItem(REGIONS_KEY) } catch {}
      navigate("/queue")
    },
    onError: () => {
      setProgress(null)
      setDirectProcessing(false)
    },
  })

  const showDownload = (!ocrMutation.isPending || session?.status === "interrupted") &&
    session !== null && session.queued > 0 &&
    (session.status === "complete" || session.status === "interrupted")

  const stepOneLocked = ocrMutation.isPending ||
    (session !== null && session.status === "in_progress") ||
    directProcessing

  const mapSummary = (s: any) => ({
    id: s.session_id || getSessionId() || "",
    total: s.total_files || folderPdfs.length,
    queued: s.queued || 0, skipped: s.skipped || 0,
    failed: s.failed || 0, remaining: s.remaining || 0,
    percent: s.percent_done || 0, status: s.status || "in_progress",
  })

  const handleResume = () => {
    setInterruptedMsg(null)
    setDownloadError(null)
    ocrMutation.mutate()
  }

  const handleStop = () => {
    abortRef.current?.abort()
    setDirectProcessing(false)
    if (session && session.queued > 0) {
      setSession(prev => prev ? { ...prev, status: "interrupted" } : prev)
    }
    if (directExtractionId) {
      getExtractionStatus(directExtractionId).then(res => {
        if (res.status === "complete") setDirectComplete(true)
      }).catch(() => {})
    }
  }

  const handleDownload = async () => {
    const id = directExtractionId || getSessionId()
    if (!id) {
      setDownloadError("No extraction or session ID found")
      return
    }
    setDownloading(true)
    setDownloadError(null)
    try {
      const mode = directExtractionId ? "extraction" : "session"
      await downloadCorrectedPDFs(id, mode)
      handleStartFresh()
    } catch (e: any) {
      setDownloadError(e.message || "Download failed")
      setDownloading(false)
    }
  }

  const handleStartFresh = async () => {
    const id = getSessionId()
    if (id && !downloading) {
      let hasCompleted = false
      try {
        const batch = await getBatchStatus(id)
        hasCompleted = batch.completed > 0
      } catch {
        hasCompleted = session?.status === "complete"
      }
      if (hasCompleted) {
        try { await downloadCorrectedPDFs(id, "session") } catch {}
      }
      try { await clearSession(id) } catch {}
    }
    if (directExtractionId && directComplete && !downloading) {
      try { await downloadCorrectedPDFs(directExtractionId, "extraction") } catch {}
    }
    clearSessionId()
    setSession(null)
    setInterruptedMsg(null)
    setDownloadError(null)
    setFolderPdfs([])
    setRegions([])
    setActiveSection(null)
    setApplyCorrectionsToAll(false)
    setDirectProcessing(false)
    setDirectComplete(false)
    setDirectFailed(false)
    setDirectExtractionId(null)
    setDirectSessionId(null)
    setBatchProgress(null)
    setDownloading(false)
    try { localStorage.removeItem(REGIONS_KEY) } catch {}
  }

  const onFolderChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files || []).filter(isSupportedFile)
    if (files.length === 0) return
    setActiveSection("folder")
    await replaceSelection(files)
  }

  const onSingleFileChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (!file || !isSupportedFile(file)) return
    setActiveSection("file")
    await replaceSelection([file])
  }

  const clearSelection = () => {
    setFolderPdfs([])
    setRegions([])
    setActiveSection(null)
    setApplyCorrectionsToAll(false)
    try { localStorage.removeItem(REGIONS_KEY) } catch {}
  }

  const replaceSelection = async (files: File[]) => {
    setFolderPdfs(files)
    setEditorOpen(false)
    setDownloadError(null)
    setDirectProcessing(false)
    setDirectComplete(false)
    setDirectFailed(false)
    setDirectExtractionId(null)
    setDirectSessionId(null)
    setBatchProgress(null)
  }

  const openEditor = () => {
    if (folderPdfs.length === 0) return
    // For images and Word docs, we still open the editor using the converted PDF preview.
    // The backend handles conversion; the editor works on the PDF representation.
    setEditorPdf(folderPdfs[0])
    setEditorOpen(true)
  }

  const onEditorSubmit = (drawnRegions: Region[]) => {
    setRegions(drawnRegions)
    setEditorOpen(false)
  }

  const canSubmitOcr = (folderPdfs.length > 0 && regions.length > 0) || !!getSessionId()

  if (editorOpen && editorPdf) {
    const isSingleFile = activeSection === "file"
    const isFolderWithCorrections = activeSection === "folder" && applyCorrectionsToAll
    const isFolderPlain           = activeSection === "folder" && !applyCorrectionsToAll
    return (
      <div style={{ position:"fixed", inset:0, zIndex:1000, background: dark ? "#0f172a" : "#f8fafc" }}>
        <PDFRegionEditor
          pdfFile={editorPdf} dark={dark}
          onSubmit={onEditorSubmit} onCancel={() => setEditorOpen(false)}
          showCorrections={isSingleFile || isFolderWithCorrections}
          autoConfirmFields={isFolderPlain}
        />
      </div>
    )
  }

  const c = {
    text: dark ? "#e2e8f0" : "#1e293b",
    muted: dark ? "#94a3b8" : "#64748b",
    card: dark ? "#1e293b" : "#ffffff",
    border: dark ? "#334155" : "#e2e8f0",
    bg: dark ? "#0f172a" : "#f8fafc",
  }

  const resumeDisabled = folderPdfs.length === 0 || regions.length === 0

  const isSingleFileDirect = activeSection === "file" && folderPdfs.length === 1
  const isFolderDirect = activeSection === "folder" && applyCorrectionsToAll && folderPdfs.length > 0
  const isDirectMode = isSingleFileDirect || isFolderDirect

  // Count file types in selection
  const pdfCount = folderPdfs.filter(f => getFileExt(f.name) === ".pdf").length
  const wordCount = folderPdfs.filter(f => [".docx", ".doc"].includes(getFileExt(f.name))).length
  const imageCount = folderPdfs.filter(f => [".png", ".jpg", ".jpeg", ".tiff", ".tif", ".bmp", ".gif", ".webp"].includes(getFileExt(f.name))).length

  return (
    <div style={{ maxWidth:"680px", margin:"0 auto", padding:"2rem", color: c.text }}>
      <h1 style={{ marginBottom:"0.5rem" }}>Upload Document</h1>

      {useLlm === null ? (
        <p style={{ color: c.muted, fontSize:"14px" }}>Loading…</p>
      ) : useLlm ? (
        <LlmSchemaUpload />
      ) : (
        <>
      <p style={{ color: c.muted, marginBottom:"1.5rem", fontSize:"14px" }}>
        Select a folder of documents and draw the fields to extract — handled directly via OCR, no LLM involved.
        Supports <strong>PDF</strong>, <strong>Word (.docx/.doc)</strong>, and <strong>images (.png, .jpg, .tiff, etc.)</strong>.
        Word and image files are automatically converted to PDF before processing.
      </p>

      <>
      <div style={{ display:"flex", alignItems:"center", justifyContent:"space-between",
        marginBottom:"1rem", padding:"10px 14px", background: c.bg, borderRadius:"8px",
        border:`1px solid ${c.border}` }}>
        <span style={{ fontSize:"12px", color: c.muted }}>Token balance</span>
        <span style={{ fontSize:"16px", fontWeight:700, color:"#16a34a" }}>
          {tokenBalance === null ? "…" : tokenBalance}
        </span>
      </div>

      {insufficientTokens && (
        <div style={{ marginBottom:"1rem", padding:"10px 14px", background:"rgba(220,38,38,.06)",
          border:"1px solid rgba(220,38,38,.3)", borderRadius:"8px", fontSize:"12px", color:"#dc2626" }}>
          ⚠ Ran out of tokens partway through this batch — the remaining files weren't processed.
          Top up on the <a href="/select-plan" style={{ color:"#dc2626", fontWeight:600 }}>plan picker</a>,
          then use "Resume Upload" below to continue with exactly the files that are left.
        </div>
      )}

          {hasSessionId && !session && !ocrMutation.isPending && !directProcessing && !directComplete && (
            <div style={{ marginBottom:"1rem", padding:"12px 14px",
              background:"rgba(59,130,246,.08)", borderRadius:"8px",
              border:"1px solid rgba(59,130,246,.3)" }}>
              <div style={{ fontSize:"13px", fontWeight:700,
                color:"#3b82f6", marginBottom:"6px" }}>
                ↩ Previous upload session found
              </div>
              <div style={{ fontSize:"12px", color: c.muted, marginBottom:"10px" }}>
                An interrupted folder upload was found. You can resume it — already-queued
                files will be skipped automatically. Or start fresh to clear the session.
              </div>
              <div style={{ display:"flex", gap:"8px", flexWrap:"wrap" }}>
                <button
                  onClick={() => ocrMutation.mutate()}
                  disabled={resumeDisabled}
                  style={{
                    padding:"7px 16px",
                    background: resumeDisabled ? "#94a3b8" : "#3b82f6",
                    color:"#fff", border:"none", borderRadius:"6px",
                    fontSize:"13px", fontWeight:600,
                    cursor: resumeDisabled ? "not-allowed" : "pointer",
                    opacity: resumeDisabled ? 0.5 : 1,
                  }}
                >
                  ↩ Resume Previous Upload
                </button>
                <button onClick={handleStartFresh}
                  style={{ padding:"7px 16px", background:"transparent",
                    color: c.muted, border:`1px solid ${c.border}`,
                    borderRadius:"6px", fontSize:"13px", cursor:"pointer" }}>
                  Start Fresh
                </button>
              </div>
              {folderPdfs.length === 0 && (
                <p style={{ fontSize:"11px", color:"#dc2626", marginTop:"8px" }}>
                  ⚠ Re-select the same folder to enable Resume — the files need to
                  be present so their hashes can be checked for deduplication.
                </p>
              )}
              {regions.length === 0 && folderPdfs.length > 0 && (
                <p style={{ fontSize:"11px", color:"#f59e0b", marginTop:"8px" }}>
                  ⚠ Field regions not found — open the editor in Step 2 to restore or re-draw them.
                </p>
              )}
            </div>
          )}

          <div style={{ marginBottom:"1.5rem", padding:"1rem", background: c.bg,
            borderRadius:"8px", border:`1px solid ${c.border}`,
            opacity: stepOneLocked ? 0.6 : 1, pointerEvents: stepOneLocked ? "none" : "auto" }}>
            <h3 style={{ marginBottom:"0.5rem", fontSize:"14px", fontWeight:600 }}>
              Step 1 — Upload Document(s)
            </h3>
            <p style={{ fontSize:"12px", color: c.muted, marginBottom:"0.75rem" }}>
              Upload a single file, or a whole folder — every document gets the same field regions
              drawn in Step 2. Word and image files are auto-converted to PDF for processing.
            </p>

            <div style={{ display:"flex", gap:"12px", flexWrap:"wrap" }}>
              {(() => {
                const disabled = stepOneLocked || activeSection === "folder" || directProcessing || directComplete
                return (
                  <div style={{ flex:"1 1 200px", padding:"12px", background: c.card,
                    borderRadius:"8px", border:`1px solid ${c.border}`,
                    opacity: disabled ? 0.45 : 1, transition:"opacity .15s" }}>
                    <div style={{ fontSize:"12px", fontWeight:600, marginBottom:"8px", color: c.muted }}>
                      Single File
                    </div>
                    <label style={{ display:"inline-block", padding:"8px 16px",
                      background: disabled ? "#94a3b8" : "#3b82f6", color:"#fff", borderRadius:"6px",
                      cursor: disabled ? "not-allowed" : "pointer", fontSize:"13px", fontWeight:600,
                      pointerEvents: disabled ? "none" : "auto" }}>
                      📄 Choose File
                      <input type="file" hidden accept=".pdf,.docx,.doc,.png,.jpg,.jpeg,.tiff,.tif,.bmp,.gif,.webp" disabled={disabled} onChange={onSingleFileChange} />
                    </label>
                  </div>
                )
              })()}

              {(() => {
                const disabled = stepOneLocked || activeSection === "file" || directProcessing || directComplete
                return (
                  <div style={{ flex:"1 1 200px", padding:"12px", background: c.card,
                    borderRadius:"8px", border:`1px solid ${c.border}`,
                    opacity: disabled ? 0.45 : 1, transition:"opacity .15s" }}>
                    <div style={{ fontSize:"12px", fontWeight:600, marginBottom:"8px", color: c.muted }}>
                      Whole Folder
                    </div>
                    <label style={{ display:"inline-block", padding:"8px 16px",
                      background: disabled ? "#94a3b8" : "#3b82f6", color:"#fff", borderRadius:"6px",
                      cursor: disabled ? "not-allowed" : "pointer", fontSize:"13px", fontWeight:600,
                      pointerEvents: disabled ? "none" : "auto" }}>
                      📁 Choose Folder
                      <input type="file" hidden disabled={disabled}
                        webkitdirectory="true" multiple
                        accept=".pdf,.docx,.doc,.png,.jpg,.jpeg,.tiff,.tif,.bmp,.gif,.webp"
                        onChange={onFolderChange} />
                    </label>
                  </div>
                )
              })()}
            </div>

            {folderPdfs.length > 0 && (
              <div style={{ marginTop:"12px" }}>
                <div style={{ display:"flex", alignItems:"center", justifyContent:"space-between", marginBottom:"6px" }}>
                  <p style={{ fontSize:"13px", color:"#16a34a", fontWeight:600, margin:0 }}>
                    ✓ {folderPdfs.length} file{folderPdfs.length > 1 ? "s" : ""} selected
                    {activeSection && <span style={{ color: c.muted, fontWeight:400 }}> ({activeSection === "file" ? "single file" : "folder"})</span>}
                  </p>
                  {!stepOneLocked && (
                    <button onClick={clearSelection}
                      style={{ fontSize:"11px", color:"#dc2626", background:"none", border:"none",
                        cursor:"pointer", textDecoration:"underline" }}>
                      ✕ Change selection
                    </button>
                  )}
                </div>
                {/* File type summary */}
                {(pdfCount > 0 || wordCount > 0 || imageCount > 0) && (
                  <div style={{ display:"flex", gap:"12px", marginBottom:"6px", fontSize:"11px", color: c.muted }}>
                    {pdfCount > 0 && <span>📄 PDF: {pdfCount}</span>}
                    {wordCount > 0 && <span>📝 Word: {wordCount}</span>}
                    {imageCount > 0 && <span>🖼️ Image: {imageCount}</span>}
                  </div>
                )}
                <div style={{ maxHeight:"120px", overflowY:"auto",
                  background: c.card, borderRadius:"6px", border:`1px solid ${c.border}`,
                  padding:"6px 10px" }}>
                  {folderPdfs.map(f => (
                    <div key={f.name} style={{ fontSize:"11px", color: c.muted,
                      padding:"2px 0", fontFamily:"monospace", display:"flex", justifyContent:"space-between" }}>
                      <span>{f.name}</span>
                      <span style={{ color:"#94a3b8" }}>
                        {fileTypeLabel(f)} · {(f.size/1024).toFixed(1)}KB
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>

          <div style={{ marginBottom:"1.5rem", padding:"1rem", background: c.bg,
            borderRadius:"8px", border:`1px solid ${c.border}`,
            opacity: folderPdfs.length === 0 || stepOneLocked ? 0.5 : 1,
            pointerEvents: folderPdfs.length === 0 || stepOneLocked ? "none" : "auto" }}>
            <h3 style={{ marginBottom:"0.5rem", fontSize:"14px", fontWeight:600 }}>
              Step 2 — Draw Field Regions
            </h3>
            <p style={{ fontSize:"12px", color: c.muted, marginBottom:"0.75rem" }}>
              The first document from your selection opens in the editor. Draw rectangles over each field
              area and label them. The same regions apply to all documents.
              {isDirectMode && (
                <span style={{ color: "#3b82f6", fontWeight: 600 }}>
                  {" "}You may also enter a <em>Correction value</em> per field —
                  the corrected output{isFolderDirect ? "s" : ""} will be generated immediately without visiting the review queue.
                </span>
              )}
            </p>

            {activeSection === "folder" && folderPdfs.length > 1 && (
              <div style={{
                marginBottom: "12px",
                padding: "10px 12px",
                background: applyCorrectionsToAll ? "rgba(59,130,246,.08)" : c.card,
                borderRadius: "8px",
                border: `1px solid ${applyCorrectionsToAll ? "rgba(59,130,246,.3)" : c.border}`,
                display: "flex",
                alignItems: "center",
                gap: "10px",
                cursor: "pointer",
              }} onClick={() => setApplyCorrectionsToAll(v => !v)}>
                <div style={{
                  width: "18px",
                  height: "18px",
                  borderRadius: "4px",
                  border: `2px solid ${applyCorrectionsToAll ? "#3b82f6" : c.border}`,
                  background: applyCorrectionsToAll ? "#3b82f6" : "transparent",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  flexShrink: 0,
                }}>
                  {applyCorrectionsToAll && <span style={{ color: "#fff", fontSize: "12px" }}>✓</span>}
                </div>
                <div>
                  <div style={{ fontSize: "13px", fontWeight: 600, color: c.text }}>
                    Apply corrections to all documents
                  </div>
                  <div style={{ fontSize: "11px", color: c.muted }}>
                    When checked, enter correction values in the editor. Every document in the folder
                    will be corrected automatically — no queue/review step needed.
                  </div>
                </div>
              </div>
            )}

            {regions.length > 0 && (
              <div style={{ marginBottom:"10px", padding:"8px 12px",
                background:"rgba(22,163,74,.08)", borderRadius:"6px",
                border:"1px solid rgba(22,163,74,.3)", fontSize:"12px", color:"#16a34a" }}>
                ✓ {regions.length} field region{regions.length > 1 ? "s" : ""} defined:&nbsp;
                {regions.map(r => r.field_name).join(", ")}
                {isDirectMode && regions.some(r => r.correction_value && r.correction_value.trim()) && (
                  <span style={{ display: "block", marginTop: "4px" }}>
                    ✓ {regions.filter(r => r.correction_value && r.correction_value.trim()).length} correction(s) ready
                  </span>
                )}
              </div>
            )}

            <button
              onClick={openEditor}
              disabled={folderPdfs.length === 0 || stepOneLocked}
              style={{ padding:"9px 20px",
                background: regions.length > 0 ? "rgba(59,130,246,.1)" : "#3b82f6",
                color: regions.length > 0 ? "#3b82f6" : "#fff",
                border: regions.length > 0 ? "1px solid #3b82f6" : "none",
                borderRadius:"6px", fontSize:"13px", fontWeight:600,
                cursor: folderPdfs.length === 0 || stepOneLocked ? "not-allowed" : "pointer" }}>
              {regions.length > 0 ? "✏️ Edit Regions" : "✏️ Open Editor"}
            </button>
          </div>

          <div style={{ padding:"1rem", background: c.bg,
            borderRadius:"8px", border:`1px solid ${c.border}`,
            opacity: stepOneLocked ? 0.7 : 1,
            pointerEvents: stepOneLocked ? "none" : "auto" }}>

            <h3 style={{ marginBottom:"0.5rem", fontSize:"14px", fontWeight:600 }}>
              Step 3 — {isDirectMode ? "Process Documents" : "Queue for OCR Extraction"}
            </h3>

            {!session && !ocrMutation.isPending && !directProcessing && folderPdfs.length > 0 && !isDirectMode && (
              <div style={{ marginBottom:"10px", fontSize:"12px", color: c.muted }}>
                {folderPdfs.length <= MAX_BATCH ? (
                  <span>{folderPdfs.length} document{folderPdfs.length > 1 ? "s" : ""} →
                  &nbsp;<strong>1 request</strong></span>
                ) : (
                  <span style={{ color:"#f59e0b" }}>
                    ⚠ {folderPdfs.length} documents will be split into&nbsp;
                    <strong>{Math.ceil(folderPdfs.length / MAX_BATCH)} chunks</strong> of
                    {MAX_BATCH} each. Each chunk is a separate request — if your
                    connection drops, all previous chunks are already safely queued.
                  </span>
                )}
              </div>
            )}

            {ocrMutation.isPending && uploadProgress && !isDirectMode && (
              <div style={{ marginBottom:"16px" }}>
                <div style={{ display:"flex", justifyContent:"space-between",
                  fontSize:"12px", color: c.muted, marginBottom:"4px" }}>
                  <span>
                    Chunk {uploadProgress.chunkIndex}/{uploadProgress.totalChunks}
                    &nbsp;·&nbsp;
                    {(session?.queued ?? 0) + uploadProgress.chunkDone} of {session?.total ?? folderPdfs.length} files queued
                  </span>
                  <span style={{ fontWeight:600, color:"#3b82f6" }}>
                    {session?.percent ?? 0}%
                  </span>
                </div>
                <div style={{ height:"8px", background: c.border,
                  borderRadius:"4px", marginBottom:"4px", overflow:"hidden" }}>
                  <div style={{
                    height:"100%", borderRadius:"4px",
                    background:"linear-gradient(90deg,#3b82f6,#06b6d4)",
                    width: `${session?.percent ?? 0}%`,
                    transition:"width .4s ease",
                  }} />
                </div>
                <div style={{ display:"flex", justifyContent:"space-between",
                  fontSize:"11px", color:"#9ca3af", marginBottom:"2px" }}>
                  <span>Current chunk</span>
                  <span>{uploadProgress.chunkDone}/{uploadProgress.chunkTotal}</span>
                </div>
                <div style={{ height:"4px", background: c.border,
                  borderRadius:"2px", overflow:"hidden" }}>
                  <div style={{
                    height:"100%", borderRadius:"2px", background:"#94a3b8",
                    width: `${Math.round(uploadProgress.chunkDone / uploadProgress.chunkTotal * 100)}%`,
                    transition:"width .2s",
                  }} />
                </div>
                <div style={{ display:"flex", gap:"16px", marginTop:"8px",
                  fontSize:"11px", flexWrap:"wrap" }}>
                  {[
                    { label:"Queued",   val: session?.queued   ?? 0, color:"#16a34a" },
                    { label:"Skipped",  val: session?.skipped  ?? 0, color:"#f59e0b" },
                    { label:"Failed",   val: session?.failed   ?? 0, color:"#dc2626" },
                    { label:"Remaining",val: session?.remaining ?? folderPdfs.length, color: c.muted },
                  ].map(p => (
                    <div key={p.label} style={{ display:"flex", gap:"4px", alignItems:"center" }}>
                      <span style={{ fontWeight:700, color: p.color }}>{p.val}</span>
                      <span style={{ color:"#9ca3af" }}>{p.label}</span>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* Direct processing spinner */}
            {directProcessing && (
              <div style={{ marginBottom:"16px", padding:"14px",
                background:"rgba(59,130,246,.08)", borderRadius:"8px",
                border:"1px solid rgba(59,130,246,.3)" }}>
                <div style={{ fontSize:"14px", fontWeight:700, color:"#3b82f6", marginBottom:"6px" }}>
                  ⏳ Processing {isFolderDirect ? `${folderPdfs.length} documents…` : "document…"}
                </div>
                <div style={{ fontSize:"12px", color: c.muted }}>
                  OCR extraction and corrections are being applied directly.
                  Please wait — the download button will appear here when ready.
                </div>

                {isFolderDirect && batchProgress && (
                  <div style={{ marginTop: "12px" }}>
                    <div style={{ display: "flex", justifyContent: "space-between", fontSize: "12px", color: c.muted, marginBottom: "4px" }}>
                      <span>{batchProgress.completed} of {batchProgress.total} completed</span>
                      <span style={{ fontWeight: 600, color: "#3b82f6" }}>{batchProgress.percent}%</span>
                    </div>
                    <div style={{ height: "8px", background: c.border, borderRadius: "4px", overflow: "hidden" }}>
                      <div style={{
                        height: "100%", borderRadius: "4px",
                        background: "linear-gradient(90deg,#3b82f6,#06b6d4)",
                        width: `${batchProgress.percent}%`,
                        transition: "width .4s ease",
                      }} />
                    </div>
                    <div style={{ display: "flex", gap: "16px", marginTop: "6px", fontSize: "11px", flexWrap: "wrap" }}>
                      <span style={{ color: "#16a34a" }}>✓ {batchProgress.completed} done</span>
                      {batchProgress.failed > 0 && <span style={{ color: "#dc2626" }}>✕ {batchProgress.failed} failed</span>}
                    </div>
                  </div>
                )}

                {!isFolderDirect && (
                  <div style={{ marginTop:"10px", height:"6px", background: c.border,
                    borderRadius:"3px", overflow:"hidden" }}>
                    <div style={{ height:"100%", borderRadius:"3px",
                      background:"linear-gradient(90deg,#3b82f6,#06b6d4)",
                      width: "60%",
                      animation: "pulse 1.5s infinite"
                    }} />
                  </div>
                )}
              </div>
            )}

            {directFailed && (
              <div style={{ marginBottom:"12px", padding:"12px 14px",
                background:"rgba(220,38,38,.08)", borderRadius:"8px",
                border:"1px solid rgba(220,38,38,.35)" }}>
                <div style={{ fontSize:"13px", fontWeight:700, color:"#dc2626", marginBottom:"6px" }}>
                  ✕ Processing Failed
                </div>
                <div style={{ fontSize:"12px", color: c.muted, marginBottom:"10px" }}>
                  The document could not be processed automatically. You can start fresh and try again.
                </div>
                <button onClick={handleStartFresh}
                  style={{ padding:"7px 16px", background:"#dc2626", color:"#fff",
                    border:"none", borderRadius:"6px", fontSize:"13px",
                    fontWeight:600, cursor:"pointer" }}>
                  Start Fresh
                </button>
              </div>
            )}

            {interruptedMsg && !ocrMutation.isPending && (
              <div style={{ marginBottom:"12px", padding:"12px 14px",
                background:"rgba(245,158,11,.08)", borderRadius:"8px",
                border:"1px solid rgba(245,158,11,.35)" }}>
                <div style={{ fontSize:"13px", fontWeight:700,
                  color:"#f59e0b", marginBottom:"8px" }}>
                  ⚠ Upload Interrupted
                </div>
                {session && (
                  <div style={{ display:"flex", gap:"16px", marginBottom:"10px",
                    fontSize:"12px", flexWrap:"wrap" }}>
                    {[
                      { label:"✓ Queued",   val:session.queued,    color:"#16a34a" },
                      { label:"⏭ Skipped",  val:session.skipped,   color:"#f59e0b" },
                      { label:"✕ Failed",   val:session.failed,    color:"#dc2626" },
                      { label:"⏳ Remaining",val:session.remaining, color: c.muted  },
                    ].map(p => (
                      <div key={p.label} style={{ fontWeight:600, color: p.color }}>
                        {p.val} {p.label}
                      </div>
                    ))}
                  </div>
                )}
                {session && (
                  <div style={{ marginBottom:"10px" }}>
                    <div style={{ height:"6px", background: c.border,
                      borderRadius:"3px", overflow:"hidden" }}>
                      <div style={{
                        height:"100%", borderRadius:"3px",
                        background:"#f59e0b",
                        width:`${session.percent}%`,
                      }} />
                    </div>
                    <div style={{ fontSize:"11px", color:"#9ca3af", marginTop:"4px" }}>
                      {session.percent}% complete at interruption
                    </div>
                  </div>
                )}
                <div style={{ fontSize:"12px", color: c.muted, marginBottom:"10px" }}>
                  {interruptedMsg}
                </div>
                <div style={{ display:"flex", gap:"8px", flexWrap:"wrap" }}>
                  <button onClick={handleResume}
                    disabled={folderPdfs.length === 0 || regions.length === 0}
                    style={{ padding:"8px 18px", background:"#f59e0b", color:"#fff",
                      border:"none", borderRadius:"6px", fontSize:"13px",
                      fontWeight:700, cursor: folderPdfs.length === 0 ? "not-allowed" : "pointer",
                      opacity: folderPdfs.length === 0 ? 0.5 : 1 }}>
                    ↩ Resume Upload
                    {session?.remaining ? ` (${session.remaining} remaining)` : ""}
                  </button>
                  <button onClick={() => navigate("/queue")}
                    style={{ padding:"8px 18px", background:"transparent",
                      color:"#3b82f6", border:"1px solid #3b82f6",
                      borderRadius:"6px", fontSize:"13px", fontWeight:600, cursor:"pointer" }}>
                    View Queue →
                  </button>
                  <button onClick={handleStartFresh}
                    style={{ padding:"8px 18px", background:"transparent",
                      color: c.muted, border:`1px solid ${c.border}`,
                      borderRadius:"6px", fontSize:"13px", cursor:"pointer" }}>
                    Start Fresh
                  </button>
                </div>
                {folderPdfs.length === 0 && (
                  <p style={{ fontSize:"11px", color:"#dc2626", marginTop:"8px" }}>
                    ⚠ Re-select the same folder to enable Resume — the files need to
                    be present so their hashes can be checked for deduplication.
                  </p>
                )}
              </div>
            )}

            {session?.status === "complete" && !ocrMutation.isPending && !interruptedMsg && !isDirectMode && (
              <div style={{ marginBottom:"12px", padding:"12px 14px",
                background:"rgba(22,163,74,.08)", borderRadius:"8px",
                border:"1px solid rgba(22,163,74,.3)" }}>
                <div style={{ fontSize:"13px", fontWeight:700,
                  color:"#16a34a", marginBottom:"6px" }}>
                  ✓ Upload Complete
                </div>
                <div style={{ display:"flex", gap:"16px", fontSize:"12px" }}>
                  <span style={{ color:"#16a34a" }}>{session.queued} queued</span>
                  {session.skipped > 0 && <span style={{ color:"#f59e0b" }}>{session.skipped} skipped</span>}
                  {session.failed  > 0 && <span style={{ color:"#dc2626" }}>{session.failed} failed</span>}
                </div>
              </div>
            )}

            {ocrMutation.isError && !interruptedMsg && (
              <div style={{ color:"#dc2626", marginBottom:"12px", fontSize:"13px",
                background:"#fef2f2", padding:"10px 12px", borderRadius:"6px",
                border:"1px solid #fecaca" }}>
                {String(ocrMutation.error)}
              </div>
            )}

            {/* Primary submit button — hidden during processing, replaced by Stop */}
            {!interruptedMsg && !directComplete && (
              ocrMutation.isPending || directProcessing ? (
                <button
                  onClick={handleStop}
                  style={{ padding:"12px 32px", background:"#dc2626", color:"#fff",
                    border:"none", borderRadius:"6px", fontSize:"15px", fontWeight:600,
                    cursor: "pointer", transition: "background .15s" }}
                  onMouseEnter={e => (e.currentTarget.style.background = "#b91c1c")}
                  onMouseLeave={e => (e.currentTarget.style.background = "#dc2626")}>
                  ⏹ Stop Processing
                </button>
              ) : (
                <button
                  onClick={() => ocrMutation.mutate()}
                  disabled={!canSubmitOcr}
                  style={{ padding:"12px 32px", background:"#16a34a", color:"#fff",
                    border:"none", borderRadius:"6px", fontSize:"15px", fontWeight:600,
                    cursor: !canSubmitOcr ? "not-allowed" : "pointer",
                    opacity: !canSubmitOcr ? 0.5 : 1 }}>
                  {getSessionId() && session && session.remaining > 0
                    ? `↩ Resume (${session.remaining} remaining)`
                    : isFolderDirect
                      ? `Process All Documents →`
                      : isSingleFileDirect
                        ? `Process Document →`
                        : `Queue ${folderPdfs.length > 0 ? folderPdfs.length : ""} Files for OCR →`}
                </button>
              )
            )}
          </div>

          {(showDownload || directComplete) && (
            <div style={{ marginBottom:"14px", padding:"14px 16px",
              background: dark ? "#0f172a" : "#f0fdf4",
              borderRadius:"8px",
              border:"1px solid rgba(22,163,74,.35)",
              position: "relative" }}>
              <button
                onClick={handleStartFresh}
                onMouseEnter={e => {
                  e.currentTarget.style.borderColor = "#dc2626"
                  e.currentTarget.style.color = "#dc2626"
                }}
                onMouseLeave={e => {
                  e.currentTarget.style.borderColor = "transparent"
                  e.currentTarget.style.color = c.muted
                }}
                style={{
                  position: "absolute",
                  top: "10px",
                  right: "10px",
                  width: "28px",
                  height: "28px",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  borderRadius: "4px",
                  border: "2px solid transparent",
                  background: "none",
                  color: c.muted,
                  fontSize: "16px",
                  fontWeight: 700,
                  cursor: "pointer",
                  transition: "all .15s",
                  lineHeight: 1,
                  padding: 0,
                }}
                title="Close and start fresh">
                ✕
              </button>

              <div style={{ fontSize:"13px", fontWeight:700,
                color:"#16a34a", marginBottom:"4px", paddingRight: "36px" }}>
                📦 Corrected Documents Ready
              </div>
              <div style={{ fontSize:"12px", color: c.muted, marginBottom:"10px" }}>
                {directComplete
                  ? (isFolderDirect
                      ? `All ${folderPdfs.length} documents have been processed and corrected. Download the results below.`
                      : "Your document has been processed and corrected. Download the result below.")
                  : session.status === "complete"
                    ? `All ${session.queued} document${session.queued > 1 ? "s" : ""} processed. Download includes corrected files and corrections.xlsx.`
                    : `${session.queued} document${session.queued > 1 ? "s" : ""} corrected so far (${session.remaining} remaining). ` +
                      `Download partial results or resume to process the rest.`}
              </div>
              {downloadError && (
                <div style={{ fontSize:"12px", color:"#dc2626",
                  marginBottom:"8px", padding:"6px 10px",
                  background:"#fef2f2", borderRadius:"6px",
                  border:"1px solid #fecaca" }}>
                  {downloadError}
                </div>
              )}
              <div style={{ display:"flex", gap:"8px", alignItems:"center",
                flexWrap:"wrap" }}>
                <button
                  onClick={handleDownload}
                  disabled={downloading}
                  style={{ display:"flex", alignItems:"center", gap:"6px",
                    padding:"9px 20px",
                    background: downloading ? "#9ca3af" : "#16a34a",
                    color:"#fff", border:"none", borderRadius:"6px",
                    fontSize:"13px", fontWeight:700,
                    cursor: downloading ? "not-allowed" : "pointer",
                    transition:"background .15s" }}>
                  {downloading ? (
                    <>⏳ Preparing download...</>
                  ) : (
                    <>⬇ Download corrected_{(directExtractionId || getSessionId())?.slice(0, 8)}.zip</>
                  )}
                </button>
                <span style={{ fontSize:"11px", color:"#9ca3af" }}>
                  corrected_pdf/pdf/ · corrected_pdf/corrections/corrections.xlsx
                </span>
              </div>
              {directComplete && (
                <div style={{ marginTop:"8px", fontSize:"11px", color:"#16a34a" }}>
                  ✓ You can now start fresh to process another batch.
                </div>
              )}
            </div>
          )}
        </>
        </>
      )}
    </div>
  )
}
