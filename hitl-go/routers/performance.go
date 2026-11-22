// hitl-go/routers/performance.go
package routers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	idb "hitl-platform/internal/db"
	imw "hitl-platform/internal/middleware"
)

func PerformanceRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(imw.Identity)
	r.Use(imw.RequireRole("debug", "admin"))

	r.Get("/workflows", handlePerfWorkflows)
	r.Get("/pipeline/summary", handlePerfPipelineSummary)
	r.Get("/pipeline/trace/{extraction_id}", handlePerfTrace)
	r.Get("/extraction/funnel", handlePerfExtractionFunnel)
	r.Get("/jobs/recent", handlePerfRecentJobs)

	return r
}

// GET /api/performance/workflows
func handlePerfWorkflows(w http.ResponseWriter, r *http.Request) {
	type workflowRow struct {
		Workflow  string  `json:"workflow"`
		AvgMS     float64 `json:"avg_ms"`
		P95MS     float64 `json:"p95_ms"`
		MaxMS     float64 `json:"max_ms"`
		CallCount int     `json:"call_count"`
		ErrorRate float64 `json:"error_rate"`
	}

	rows, err := idb.Pool().Query(r.Context(), `
		SELECT
			CASE
				WHEN method = 'POST' AND path LIKE '/api/auth/login'     THEN 'A: Login'
				WHEN method = 'POST' AND path LIKE '/api/auth/register'  THEN 'A: Register'
				WHEN method = 'POST' AND path LIKE '/api/auth/logout'    THEN 'F: Logout / Blacklist'
				WHEN method = 'POST' AND path LIKE '/api/documents/%'    THEN 'B: Upload → Extraction'
				WHEN method = 'GET'  AND path LIKE '/api/extractions/%'  THEN 'C: Poll Extraction'
				WHEN method = 'POST' AND path LIKE '/api/review/correct' THEN 'D: Correction → DPO'
				ELSE 'Other: ' || method || ' ' || path
			END AS workflow,
			ROUND(AVG(duration_ms)::numeric, 1)                                                    AS avg_ms,
			ROUND(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY duration_ms)::numeric, 1)           AS p95_ms,
			ROUND(MAX(duration_ms)::numeric, 1)                                                    AS max_ms,
			COUNT(*)                                                                                AS call_count,
			ROUND(
				100.0 * COUNT(*) FILTER (WHERE status_code >= 400) / NULLIF(COUNT(*), 0), 1
			)                                                                                       AS error_rate
		FROM audit_log
		WHERE timestamp >= now() - interval '7 days'
		GROUP BY workflow
		ORDER BY avg_ms DESC
	`)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not fetch workflow metrics")
		return
	}
	defer rows.Close()

	var results []workflowRow
	for rows.Next() {
		var r workflowRow
		if err := rows.Scan(
			&r.Workflow,
			&r.AvgMS, &r.P95MS, &r.MaxMS,
			&r.CallCount, &r.ErrorRate,
		); err != nil {
			continue
		}
		results = append(results, r)
	}
	if results == nil {
		results = []workflowRow{}
	}
	writeJSON(w, http.StatusOK, results)
}

// GET /api/performance/extraction/funnel
func handlePerfExtractionFunnel(w http.ResponseWriter, r *http.Request) {
	type funnelStats struct {
		TotalJobs       int      `json:"total_jobs"`
		Complete        int      `json:"complete"`
		Pending         int      `json:"pending"`
		Failed          int      `json:"failed"`
		Processing      int      `json:"processing"`
		SuccessRate     float64  `json:"success_rate"`
		AvgQueueWaitMS  *float64 `json:"avg_queue_wait_ms"`
		P95QueueWaitMS  *float64 `json:"p95_queue_wait_ms"`
		AvgProcessingMS *float64 `json:"avg_processing_ms"`
		P95ProcessingMS *float64 `json:"p95_processing_ms"`
		AvgTotalMS      *float64 `json:"avg_total_ms"`
	}

	var s funnelStats
	err := idb.Pool().QueryRow(r.Context(), `
		SELECT
			COUNT(*)                                                                                AS total_jobs,
			COUNT(*) FILTER (WHERE status = 'complete')                                            AS complete,
			COUNT(*) FILTER (WHERE status = 'pending')                                             AS pending,
			COUNT(*) FILTER (WHERE status = 'failed')                                              AS failed,
			COUNT(*) FILTER (WHERE status = 'processing')                                          AS processing,
			ROUND(100.0 * COUNT(*) FILTER (WHERE status = 'complete') / NULLIF(COUNT(*),0), 1)    AS success_rate,
			ROUND(AVG(
				EXTRACT(EPOCH FROM (started_at - created_at)) * 1000
			) FILTER (WHERE started_at IS NOT NULL)::numeric, 1)                                   AS avg_queue_wait_ms,
			ROUND(PERCENTILE_CONT(0.95) WITHIN GROUP (
				ORDER BY EXTRACT(EPOCH FROM (started_at - created_at)) * 1000
			) FILTER (WHERE started_at IS NOT NULL)::numeric, 1)                                   AS p95_queue_wait_ms,
			ROUND(AVG(
				EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000
			) FILTER (WHERE completed_at IS NOT NULL AND started_at IS NOT NULL)::numeric, 1)      AS avg_processing_ms,
			ROUND(PERCENTILE_CONT(0.95) WITHIN GROUP (
				ORDER BY EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000
			) FILTER (WHERE completed_at IS NOT NULL AND started_at IS NOT NULL)::numeric, 1)      AS p95_processing_ms,
			ROUND(AVG(
				EXTRACT(EPOCH FROM (completed_at - created_at)) * 1000
			) FILTER (WHERE completed_at IS NOT NULL)::numeric, 1)                                 AS avg_total_ms
		FROM extraction_jobs
		WHERE created_at >= now() - interval '7 days'
	`).Scan(
		&s.TotalJobs, &s.Complete, &s.Pending, &s.Failed, &s.Processing,
		&s.SuccessRate,
		&s.AvgQueueWaitMS, &s.P95QueueWaitMS,
		&s.AvgProcessingMS, &s.P95ProcessingMS,
		&s.AvgTotalMS,
	)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not fetch extraction funnel")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// GET /api/performance/pipeline/summary
func handlePerfPipelineSummary(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Stage     string  `json:"stage"`
		Step      string  `json:"step"`
		AvgMS     float64 `json:"avg_ms"`
		P95MS     float64 `json:"p95_ms"`
		MaxMS     float64 `json:"max_ms"`
		CallCount int     `json:"call_count"`
	}

	rows, err := idb.Pool().Query(r.Context(), `
		SELECT
			stage,
			step,
			ROUND(AVG(duration_ms)::numeric, 1)                                          AS avg_ms,
			ROUND(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY duration_ms)::numeric, 1) AS p95_ms,
			ROUND(MAX(duration_ms)::numeric, 1)                                          AS max_ms,
			COUNT(*)                                                                      AS call_count
		FROM performance_logs
		WHERE recorded_at >= now() - interval '7 days'
		GROUP BY stage, step
		ORDER BY avg_ms DESC
	`)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not fetch pipeline summary")
		return
	}
	defer rows.Close()

	var results []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Stage, &r.Step, &r.AvgMS, &r.P95MS, &r.MaxMS, &r.CallCount); err != nil {
			continue
		}
		results = append(results, r)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

// GET /api/performance/pipeline/trace/:extraction_id
func handlePerfTrace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "extraction_id")

	type row struct {
		Stage      string    `json:"stage"`
		Step       string    `json:"step"`
		FieldName  *string   `json:"field_name"`
		DurationMS float64   `json:"duration_ms"`
		RecordedAt time.Time `json:"recorded_at"`
	}

	rows, err := idb.Pool().Query(r.Context(), `
		SELECT stage, step, field_name, duration_ms, recorded_at
		FROM performance_logs
		WHERE extraction_id = $1
		ORDER BY recorded_at ASC
	`, id)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not fetch trace")
		return
	}
	defer rows.Close()

	var results []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Stage, &r.Step, &r.FieldName, &r.DurationMS, &r.RecordedAt); err != nil {
			continue
		}
		results = append(results, r)
	}
	if results == nil {
		results = []row{}
	}

	var total float64
	for _, r := range results {
		total += r.DurationMS
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"extraction_id": id,
		"steps":         results,
		"total_ms":      total,
	})
}

// GET /api/performance/jobs/recent
func handlePerfRecentJobs(w http.ResponseWriter, r *http.Request) {
	type row struct {
		ExtractionID string    `json:"extraction_id"`
		Status       string    `json:"status"`
		QueueWaitMS  *float64  `json:"queue_wait_ms"`
		ProcessingMS *float64  `json:"processing_ms"`
		CreatedAt    time.Time `json:"created_at"`
	}

	rows, err := idb.Pool().Query(r.Context(), `
		SELECT
			extraction_id,
			status,
			ROUND(EXTRACT(EPOCH FROM (started_at - created_at)) * 1000, 1)   AS queue_wait_ms,
			ROUND(EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000, 1) AS processing_ms,
			created_at
		FROM extraction_jobs
		ORDER BY created_at DESC
		LIMIT 20
	`)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not fetch recent jobs")
		return
	}
	defer rows.Close()

	var results []row
	for rows.Next() {
		var r row
		if err := rows.Scan(
			&r.ExtractionID, &r.Status,
			&r.QueueWaitMS, &r.ProcessingMS, &r.CreatedAt,
		); err != nil {
			// log.Printf("[perf] scan error: %v", err)

			continue
		}
		results = append(results, r)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}
