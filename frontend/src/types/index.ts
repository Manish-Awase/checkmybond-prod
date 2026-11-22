// frontend/src/types/index.ts
// Shared TypeScript types used across pages and api.ts

// ── Documents ─────────────────────────────────────────────────────────────────
export interface DocumentRecord {
  doc_id:        string
  doc_hash:      string
  filename:      string
  status:        "pending" | "parsing" | "embedding" | "ready" | "failed"
  doc_type:      "digital" | "scanned" | "mixed"
  page_count:    number
  user_id:       string
  extraction_id: string
  created_at:    string
  updated_at:    string
}

// ── Extractions ───────────────────────────────────────────────────────────────
export interface FieldResult {
  field_name:        string
  value:             string
  confidence_score:  number
  context_relevance: number
  groundedness:      number
  answer_relevance:  number
  decision:          "auto_accept" | "human_review" | "re_extract" | "fail"
  prompt_id:         string
  retrieved_chunks:  string[]
}

export interface ExtractionResult {
  extraction_id: string
  status:        "pending" | "processing" | "complete" | "failed"
  fields:        FieldResult[]
  error:         string | null
  created_at:    string
  started_at:    string | null
  completed_at:  string | null
  sla_deadline:  string
}

// ── Queue ─────────────────────────────────────────────────────────────────────
export interface QueueItem {
  id:           string
  doc_hash:     string | null
  schema_id:    string | null
  status:       string
  pdf_path:     string | null
  created_at:   string
  completed_at: string | null
  sla_deadline: string
}

// ── Schemas ───────────────────────────────────────────────────────────────────
export interface SchemaField {
  name:        string
  field_type:  string
  aliases:     string[]
  required:    boolean
  description?: string
}

export interface SchemaListItem {
  schema_id:   string
  schema_name: string
  version:     string
  created_at:  string
  created_by:  string
}

// ── Corrections ───────────────────────────────────────────────────────────────
export interface CorrectionRecord {
  id:               string
  extraction_id:    string
  field_name:       string
  original_value:   string
  corrected_value:  string
  failure_type:     string
  analyst_id:       string
  confidence_score: number | null
  corrected_at:     string
}

// ── Monitoring ────────────────────────────────────────────────────────────────
export interface CorrectionStats {
  total:           number
  avg_confidence:  number | null
  dpo_pairs_ready: number
  by_failure_type: Record<string, number>
  by_prompt_id:    Record<string, number>
}

export interface DriftEntry {
  field_name:   string
  recent_avg:   number
  baseline_avg: number
  drift:        number
  alert:        boolean
}

export interface MonitoringStats {
  correction_stats: CorrectionStats
  drift_report:     DriftEntry[]
  queue_depth:      number
  escalations:      number
}

// ── Auth ──────────────────────────────────────────────────────────────────────
export interface UserRecord {
  user_id:    string
  username:   string
  email:      string
  role:       string
  is_active:  boolean
  created_at: string
  last_login: string | null
}
