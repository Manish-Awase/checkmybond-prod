// frontend/src/components/LlmSchemaUpload.tsx
//
// Shown on the Upload page instead of the OCR region-drawing flow when
// GET /api/documents/config returns use_llm: true. Takes a PDF + an Excel
// schema file, parses the Excel client-side into the same JSON shape
// backend/services/schema_service.py's SchemaDefinition/FieldDefinition
// expect, and posts both to the existing POST /api/documents/upload
// endpoint — that endpoint and everything downstream of it (schema
// registry, Celery's process_document task, the extraction pipeline)
// don't need to know or care that the schema originated as .xlsx; they
// only ever see the resulting JSON bytes.
import { useState } from "react"
import { useNavigate } from "react-router-dom"
import * as XLSX from "xlsx"
import { uploadDocument } from "../lib/api"

const VALID_FIELD_TYPES = ["currency", "date", "percentage", "ein", "text", "number", "boolean"]

interface ParsedField {
  name: string
  field_type: string
  aliases: string[]
  required: boolean
  description?: string
}

// Expected Excel columns (header row, case-insensitive):
//   name | field_type | aliases | required | description
// aliases: comma-separated. required: TRUE/FALSE (blank = TRUE).
function parseSchemaExcel(buffer: ArrayBuffer): { fields: ParsedField[]; errors: string[] } {
  const wb = XLSX.read(buffer, { type: "array" })
  const sheet = wb.Sheets[wb.SheetNames[0]]
  const rows: any[] = XLSX.utils.sheet_to_json(sheet, { defval: "" })

  const errors: string[] = []
  const fields: ParsedField[] = []

  rows.forEach((row, i) => {
    const rowNum = i + 2 // header is row 1
    const get = (key: string) => {
      const k = Object.keys(row).find(k => k.toLowerCase().trim() === key)
      return k ? String(row[k]).trim() : ""
    }
    const name = get("name")
    const fieldType = get("field_type").toLowerCase()
    if (!name) { errors.push(`Row ${rowNum}: missing "name"`); return }
    if (!VALID_FIELD_TYPES.includes(fieldType)) {
      errors.push(`Row ${rowNum}: "${fieldType || "(blank)"}" is not a valid field_type — must be one of ${VALID_FIELD_TYPES.join(", ")}`)
      return
    }
    const aliasesRaw = get("aliases")
    const requiredRaw = get("required").toLowerCase()
    fields.push({
      name,
      field_type: fieldType,
      aliases: aliasesRaw ? aliasesRaw.split(",").map(a => a.trim()).filter(Boolean) : [],
      required: requiredRaw === "" ? true : requiredRaw === "true",
      description: get("description") || undefined,
    })
  })

  if (fields.length === 0 && errors.length === 0) {
    errors.push("No rows found — the sheet needs a header row (name, field_type, aliases, required, description) plus at least one field.")
  }

  return { fields, errors }
}

export default function LlmSchemaUpload() {
  const navigate = useNavigate()

  const [pdfFile, setPdfFile]       = useState<File | null>(null)
  const [schemaFile, setSchemaFile] = useState<File | null>(null)
  const [parsedFields, setParsedFields] = useState<ParsedField[] | null>(null)
  const [parseErrors, setParseErrors]   = useState<string[]>([])
  const [submitting, setSubmitting]     = useState(false)
  const [submitError, setSubmitError]   = useState("")

  const onSchemaFileChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (!file) return
    setSchemaFile(file)
    setSubmitError("")
    try {
      const buf = await file.arrayBuffer()
      const { fields, errors } = parseSchemaExcel(buf)
      setParsedFields(fields)
      setParseErrors(errors)
    } catch {
      setParsedFields(null)
      setParseErrors(["Could not read this file — make sure it's a valid .xlsx"])
    }
  }

  const canSubmit = !!pdfFile && !!parsedFields && parsedFields.length > 0 && parseErrors.length === 0 && !submitting

  const handleSubmit = async () => {
    if (!canSubmit || !pdfFile || !parsedFields) return
    setSubmitting(true)
    setSubmitError("")
    try {
      const schemaJson = JSON.stringify({
        schema_name: pdfFile.name.replace(/\.pdf$/i, ""),
        version: "1.0",
        fields: parsedFields,
      })
      const form = new FormData()
      form.append("pdf", pdfFile)
      form.append("schema", new Blob([schemaJson], { type: "application/json" }), "schema.json")

      const res = await uploadDocument(form)
      navigate(`/review/${res.extraction_id}`)
    } catch (err) {
      setSubmitError(err instanceof Error ? err.message : "Could not reach the server")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div style={{ maxWidth: "560px" }}>
      <div style={{ padding: "10px 14px", background: "rgba(59,130,246,.06)",
        border: "1px solid rgba(59,130,246,.25)", borderRadius: "8px",
        fontSize: "12px", marginBottom: "1rem" }}>
        LLM extraction mode is on (<code>USE_LLM=true</code>). Upload a PDF and an Excel schema
        instead of drawing OCR regions — fields are located and extracted via an LLM against your schema.
      </div>

      <div style={{ marginBottom: "1rem" }}>
        <label style={{ fontSize: "12px", fontWeight: 600, display: "block", marginBottom: "6px" }}>
          1. PDF document
        </label>
        <input type="file" accept=".pdf" onChange={e => setPdfFile(e.target.files?.[0] || null)} />
        {pdfFile && <p style={{ fontSize: "11px", color: "#16a34a", marginTop: "4px" }}>✓ {pdfFile.name}</p>}
      </div>

      <div style={{ marginBottom: "1rem" }}>
        <label style={{ fontSize: "12px", fontWeight: 600, display: "block", marginBottom: "6px" }}>
          2. Schema (.xlsx) — columns: <code>name</code>, <code>field_type</code>, <code>aliases</code>, <code>required</code>, <code>description</code>
        </label>
        <p style={{ fontSize: "11px", color: "#64748b", marginBottom: "6px" }}>
          field_type must be one of: {VALID_FIELD_TYPES.join(", ")}. aliases is comma-separated and optional.
          required defaults to TRUE if left blank.
        </p>
        <input type="file" accept=".xlsx,.xls" onChange={onSchemaFileChange} />
        {schemaFile && parseErrors.length === 0 && parsedFields && (
          <p style={{ fontSize: "11px", color: "#16a34a", marginTop: "4px" }}>
            ✓ {schemaFile.name} — {parsedFields.length} field{parsedFields.length === 1 ? "" : "s"} parsed
          </p>
        )}
        {parseErrors.length > 0 && (
          <div style={{ marginTop: "6px", fontSize: "11px", color: "#dc2626" }}>
            {parseErrors.map((e, i) => <div key={i}>⚠ {e}</div>)}
          </div>
        )}
      </div>

      {parsedFields && parsedFields.length > 0 && parseErrors.length === 0 && (
        <div style={{ marginBottom: "1rem", fontSize: "11px", background: "#f8fafc",
          border: "1px solid #e2e8f0", borderRadius: "6px", padding: "8px 10px" }}>
          {parsedFields.map(f => (
            <div key={f.name} style={{ padding: "2px 0" }}>
              <strong>{f.name}</strong> ({f.field_type}){f.required ? "" : " — optional"}
              {f.aliases.length > 0 && <span style={{ color: "#64748b" }}> · aliases: {f.aliases.join(", ")}</span>}
            </div>
          ))}
        </div>
      )}

      {submitError && (
        <div style={{ marginBottom: "1rem", fontSize: "12px", color: "#dc2626",
          background: "rgba(220,38,38,.06)", border: "1px solid rgba(220,38,38,.3)",
          borderRadius: "6px", padding: "8px 12px" }}>
          {submitError}
        </div>
      )}

      <button onClick={handleSubmit} disabled={!canSubmit}
        style={{ padding: "10px 24px", background: "#3b82f6", color: "#fff", border: "none",
          borderRadius: "6px", fontSize: "13px", fontWeight: 700,
          cursor: canSubmit ? "pointer" : "not-allowed", opacity: canSubmit ? 1 : 0.5 }}>
        {submitting ? "Uploading…" : "Extract Fields →"}
      </button>
    </div>
  )
}