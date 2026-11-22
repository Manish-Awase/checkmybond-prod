// frontend/src/lib/api.ts
import type {
  DocumentRecord,
  ExtractionResult,
  QueueItem,
  SchemaListItem,
  CorrectionRecord,
  MonitoringStats,
  UserRecord,
} from "../types"

const BASE_URL = ""

export async function apiFetch<T = unknown>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const stored = localStorage.getItem("hitl_auth")
  const user   = stored ? JSON.parse(stored) : null
  const token  = user?.access_token

  const isFormData = options.body instanceof FormData

  const response = await fetch(`${BASE_URL}${path}`, {
    ...options,
    headers: {
      ...(!isFormData ? { "Content-Type": "application/json" } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...options.headers,
    },
  })

  if (response.status === 401) {
    localStorage.removeItem("hitl_auth")
    window.location.href = "/login"
    throw new Error("Unauthorized")
  }

  if (!response.ok) {
    const error = await response.json().catch(() => ({}))
    throw new Error(error.detail || `HTTP error ${response.status}`)
  }

  return response.json() as Promise<T>
}

// Google OAuth
export const oauthSessionRequest = (token: string) =>
  fetch(`${BASE_URL}/api/auth/oauth/session?token=${encodeURIComponent(token)}`)

// ── Auth ──────────────────────────────────────────────────────────────────────
export const getMe = () => apiFetch<UserRecord>("/api/auth/me")
export const listUsers = () => apiFetch<UserRecord[]>("/api/auth/users")
export const deactivateUser = (userId: string) =>
  apiFetch(`/api/auth/users/${userId}/deactivate`, { method: "PATCH" })

// ── Documents ─────────────────────────────────────────────────────────────────
export const getDocuments = () => apiFetch<DocumentRecord[]>("/api/documents")

export const uploadDocument = (formData: FormData) =>
  apiFetch<{ extraction_id: string; doc_id: string; status: string }>(
    "/api/documents/upload", { method: "POST", body: formData },
  )

export const getDocumentConfig = () =>
  apiFetch<{ use_llm: boolean }>("/api/documents/config")

export const getExtractionStatus = (extractionId: string) =>
  apiFetch<{ status: string }>(
    `/api/documents/extraction-status?extraction_id=${extractionId}`,
  )
export const markExtractionComplete = (extractionId: string) =>
  apiFetch<{ status: string }>(
    `/api/extractions/${extractionId}/complete`, { method: "POST" }
  )

export const getBatchStatus = (sessionId: string) =>
  apiFetch<{ completed: number; failed: number; total: number; percent: number }>(
    `/api/documents/batch-status?session_id=${sessionId}`,
  )

export async function uploadOCR(formData: FormData, signal?: AbortSignal): Promise<Response> {
  const stored = localStorage.getItem("hitl_auth")
  const token  = stored ? JSON.parse(stored)?.access_token : null
  const res = await fetch(`${BASE_URL}/api/documents/upload-ocr`, {
    method: "POST",
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    body: formData,
    signal,
  })
  if (res.status === 401) {
    localStorage.removeItem("hitl_auth")
    window.location.href = "/login"
    throw new Error("Unauthorized")
  }
  return res
}

// ── Extractions ───────────────────────────────────────────────────────────────
export const getExtraction = (extractionId: string) =>
  apiFetch<ExtractionResult>(`/api/extractions/${extractionId}`)

// ── Review / Corrections ──────────────────────────────────────────────────────
export const submitCorrection = (payload: {
  extraction_id:   string
  field_name:      string
  original_value:  string
  corrected_value: string
  failure_type:    string
  schema_id?:      string
  prompt_id?:      string
  retrieved_chunk_ids?: string[]
  confidence_score?:    number
  context_relevance?:   number
  answer_relevance?:    number
  groundedness?:        number
}) =>
  apiFetch<{ status: string; correction_id: string; dpo_pair_exported: boolean }>(
    "/api/review/correct", { method: "POST", body: JSON.stringify(payload) },
  )

// helper function to get session id by extraction id
export const getSessionByExtraction = (extractionId: string) =>
  apiFetch<{ session_id: string; filename: string }>(
    `/api/documents/session-by-extraction?extraction_id=${extractionId}`,
  )

export const getCorrections = () =>
  apiFetch<CorrectionRecord[]>("/api/review/correct")

// ── Queue ─────────────────────────────────────────────────────────────────────
export const getQueueItems = () => apiFetch<QueueItem[]>("/api/queue")
export const getAllJobs = () => apiFetch<QueueItem[]>("/api/queue/all")
export const getHITLQueue = () =>
  apiFetch<{ items: QueueItem[]; queue_depth: number; escalations: any[] }>("/api/hitl/queue")
export const getNextReviewItem = () =>
  apiFetch<{ item: QueueItem | null; message?: string }>("/api/hitl/queue/next")
export const clearEscalations = () =>
  apiFetch<{ cleared: boolean }>("/api/hitl/escalations/clear", { method: "POST" })
export const clearOverdueJobs = () =>
  apiFetch<{ cleared: boolean }>("/api/queue/clear-overdue", { method: "POST" })

// ── Schemas ───────────────────────────────────────────────────────────────────
export const getSchemas = () => apiFetch<SchemaListItem[]>("/api/schemas")

// ── Monitoring / Performance ──────────────────────────────────────────────────
export const getMonitoringStats = () => apiFetch<MonitoringStats>("/api/monitoring/stats")

export const getPerformanceWorkflows = () =>
  apiFetch<any[]>("/api/performance/workflows")

export const getExtractionFunnel = () =>
  apiFetch<any>("/api/performance/extraction/funnel")

export const getPipelineSummary = () =>
  apiFetch<any[]>("/api/performance/pipeline/summary")

export const getRecentJobs = () =>
  apiFetch<any[]>("/api/performance/jobs/recent")

export const getPipelineTrace = (traceId: string) =>
  apiFetch<any>(`/api/performance/pipeline/trace/${traceId}`)

// ── Payment ─────────────────────────────────────────────────────────────────────
export const claimFreePlan = () =>
  apiFetch<{ status: string }>("/api/payment/claim-free", { method: "POST" })

// ── Profile ────────────────────────────────────────────────────────────────────
export const getTokenBalance = () =>
  apiFetch<{ token_balance: number }>("/api/profile/balance")

export const submitFeedback = (payload: { kind: "feedback" | "complaint"; message: string }) =>
  apiFetch("/api/profile/feedback", { method: "POST", body: JSON.stringify(payload) })

export const deleteAccount = (payload: { reason?: string; rating?: number }) =>
  apiFetch("/api/profile/delete-account", { method: "POST", body: JSON.stringify(payload) })

// ── Account (owner Basic Auth) ────────────────────────────────────────────────
// Raw Response variants — let the caller handle 401/403/status checks
// (AccountsPage needs this so a wrong owner password doesn't wipe the
// regular user's JWT session via apiFetch's global 401 handler.)

export async function ownerListUsersRaw(credentials: string): Promise<Response> {
  return fetch(`${BASE_URL}/api/account/users`, {
    headers: { Authorization: `Basic ${credentials}` },
  })
}

export async function ownerToggleUserRaw(userId: string, credentials: string): Promise<Response> {
  return fetch(`${BASE_URL}/api/account/users/${userId}/toggle`, {
    method: "POST",
    headers: { Authorization: `Basic ${credentials}` },
  })
}

// Convenience wrappers that parse JSON (used by pages that don't need
// to inspect status codes themselves)
export const ownerListUsers = (credentials: string) =>
  ownerListUsersRaw(credentials).then(r => r.json())

export const ownerToggleUser = (userId: string, credentials: string) =>
  ownerToggleUserRaw(userId, credentials).then(r => r.json())

export interface HealthResponse {
  status: "healthy" | "degraded" | "unhealthy"
  checkedAt: string
  components: { name: string; healthy: boolean; detail?: string }[]
  requests?: { totalRequests: number; errorCount: number; errorRatePct: number; avgDurationMs: number }
  payments?: { totalAttempts: number; failedCount: number; failureRatePct: number }
}

export async function ownerHealthCheck(credentials: string): Promise<HealthResponse> {
  const res = await fetch(`${BASE_URL}/api/account/health`, {
    headers: { Authorization: `Basic ${credentials}` },
  })
  if (res.status === 401) {
    const err = new Error("Invalid owner credentials") as any
    err.status = 401
    throw err
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body.error || `HTTP ${res.status}`)
  }
  return res.json()
}

// ── OCR Session ───────────────────────────────────────────────────────────────
export async function downloadCorrectedPDFs(
  id: string,
  mode: "session" | "extraction" = "session"
): Promise<void> {
  const stored = localStorage.getItem("hitl_auth")
  const token  = stored ? JSON.parse(stored)?.access_token : null
  const paramName = mode === "extraction" ? "extraction_id" : "session_id"
  const res = await fetch(
    `${BASE_URL}/api/documents/download-corrected?${paramName}=${encodeURIComponent(id)}`,
    { headers: token ? { Authorization: `Bearer ${token}` } : {} }
  )
  if (res.status === 401) {
    localStorage.removeItem("hitl_auth")
    window.location.href = "/login"
    throw new Error("Unauthorized")
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({}))
    throw new Error(err.detail || `Download failed: ${res.status}`)
  }
  const disposition = res.headers.get("Content-Disposition") || ""
  const match       = disposition.match(/filename="([^"]+)"/)
  const filename    = match ? match[1] : `corrected_pdfs_${id.slice(0, 8)}.zip`
  const blob = await res.blob()
  const url  = URL.createObjectURL(blob)
  const a    = document.createElement("a")
  a.href     = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

export async function clearSession(sessionId: string): Promise<void> {
  const stored = localStorage.getItem("hitl_auth")
  const token  = stored ? JSON.parse(stored)?.access_token : null
  const res = await fetch(
    `${BASE_URL}/api/documents/session?session_id=${encodeURIComponent(sessionId)}`,
    { method: "DELETE", headers: token ? { Authorization: `Bearer ${token}` } : {} }
  )
  if (res.status === 401) {
    localStorage.removeItem("hitl_auth")
    window.location.href = "/login"
    throw new Error("Unauthorized")
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({}))
    throw new Error(err.detail || `Clear session failed: ${res.status}`)
  }
}
