// internal/backup/backup.go
// Snapshots all DB tables to export/db_backup_<timestamp>.json on shutdown.
// Go equivalent of utils/db_backup.py.
package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	idb "hitl-platform/internal/db"
)

var exportDir = func() string {
	if d := os.Getenv("EXPORT_DIR"); d != "" {
		return d
	}
	return "export"
}()

func Run(ctx context.Context) {
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		log.Printf("[backup] cannot create export dir: %v", err)
		return
	}

	tables := map[string]string{
		"users":           `SELECT id, username, email, role, is_active, created_at, last_login FROM users ORDER BY created_at DESC`,
		"documents":       `SELECT doc_id, doc_hash, filename, status, doc_type, page_count, user_id, created_at FROM documents ORDER BY created_at DESC`,
		"schema_registry": `SELECT schema_id, schema_name, version, created_at, created_by FROM schema_registry`,
		"extraction_jobs": `SELECT extraction_id, doc_hash, schema_id, pdf_path, status, error, created_at, started_at, completed_at, sla_deadline FROM extraction_jobs ORDER BY created_at DESC`,
		"extractions":     `SELECT extraction_id, doc_id, schema_id, status, auto_accepted, queued_for_review, failed_count, overall_confidence, created_at, completed_at FROM extractions ORDER BY created_at DESC`,
		"corrections":     `SELECT id, extraction_id, field_name, original_value, corrected_value, failure_type, analyst_id, confidence_score, corrected_at FROM corrections ORDER BY corrected_at DESC`,
		"audit_log":       `SELECT id, path, method, user_id, status_code, duration_ms, timestamp FROM audit_log ORDER BY id DESC LIMIT 1000`,
		"prompt_versions": `SELECT prompt_id, prompt_name, version_tag, is_active, created_at, created_by FROM prompt_versions`,
	}

	snapshot := map[string]any{
		"backed_up_at": time.Now().UTC().Format(time.RFC3339),
		"counts":       map[string]int{},
	}

	for table, query := range tables {
		rows, err := idb.BackupTable(ctx, query)
		if err != nil {
			log.Printf("[backup] table %s failed: %v", table, err)
			rows = []idb.BackupRow{} // include empty rather than skip
		}
		snapshot[table] = rows
		snapshot["counts"].(map[string]int)[table] = len(rows)
	}

	ts    := time.Now().UTC().Format("20060102_150405")
	fpath := fmt.Sprintf("%s/db_backup_%s.json", exportDir, ts)

	f, err := os.Create(fpath)
	if err != nil {
		log.Printf("[backup] create file failed: %v", err)
		return
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snapshot); err != nil {
		log.Printf("[backup] write failed: %v", err)
		return
	}

	counts := snapshot["counts"].(map[string]int)
	log.Printf("[backup] written → %s  (jobs=%d corrections=%d users=%d)",
		fpath, counts["extraction_jobs"], counts["corrections"], counts["users"])
}
