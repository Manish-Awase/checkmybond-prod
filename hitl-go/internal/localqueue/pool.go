// hitl-go/internal/localqueue/pool.go
//
// Replaces Celery + Redis entirely. Instead of pushing a message onto a
// Redis broker for a permanently-running Celery worker to consume, this
// runs a bounded pool of goroutines, each processing one job at a time by
// spawning a short-lived Python subprocess (backend/workers/cli.py) that
// exits when the job is done. No permanently-open worker process required.
package localqueue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Job is the JSON payload sent to workers/cli.py on stdin.
type Job struct {
	ID         string `json:"id"`
	TaskType   string `json:"task_type"`
	RetryCount int    `json:"retry_count"`

	// process_document fields
	ExtractionID string `json:"extraction_id,omitempty"`
	PdfPath      string `json:"pdf_path,omitempty"`
	SchemaID     string `json:"schema_id,omitempty"`
	DocHash      string `json:"doc_hash,omitempty"`

	// process_single_ocr_document fields (reuses ExtractionID/PdfPath/SchemaID/DocHash
	// above, and SerialNumber below from the correction-task fields)
	RegionsJSON string `json:"regions_json,omitempty"`

	// apply_correction_to_pdf fields
	CorrectionID     string `json:"correction_id,omitempty"`
	FieldName        string `json:"field_name,omitempty"`
	OriginalValue    string `json:"original_value,omitempty"`
	CorrectedValue   string `json:"corrected_value,omitempty"`
	SerialNumber     int    `json:"serial_number,omitempty"`
	OriginalFilename string `json:"original_filename,omitempty"`
}

// Result mirrors cli.py's stdout JSON.
type Result struct {
	Status string `json:"status"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Store persists job outcomes. Implemented against Postgres in store.go.
type Store interface {
	MarkFailed(ctx context.Context, extractionID string, errStr string) error
}

type Pool struct {
	workers    int
	queue      chan Job
	pythonExe  string
	backendDir string // cwd for the subprocess — backend/, so `python -m workers.cli` resolves
	maxRetries int
	store      Store

	wg      sync.WaitGroup
	mu      sync.Mutex
	running map[string]*exec.Cmd
	ctx     context.Context
	cancel  context.CancelFunc
}

var defaultPool *Pool

// RunSync runs a job immediately using the pool's python executable and
// returns the parsed Result. Used for startup/periodic tasks like
// "flush_pending_results".
func RunSync(ctx context.Context, job Job) (Result, error) {
	var empty Result
	if defaultPool == nil {
		return empty, fmt.Errorf("localqueue not initialized")
	}

	payload, _ := json.Marshal(job)
	cmd := exec.CommandContext(ctx, defaultPool.pythonExe, "-m", "workers.cli")
	cmd.Dir = defaultPool.backendDir
	cmd.Env = os.Environ()
	cmd.Stdin = bytes.NewReader(payload)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if err := cmd.Run(); err != nil {
		if stderr := stderrBuf.String(); stderr != "" {
			log.Printf("[localqueue] RunSync STDERR: %s", stderr)
		}
		return empty, fmt.Errorf("subprocess error: %v", err)
	}

	var res Result
	if err := json.Unmarshal(stdoutBuf.Bytes(), &res); err != nil {
		return empty, fmt.Errorf("invalid JSON from cli.py: %v (stdout: %q)", err, stdoutBuf.String())
	}
	return res, nil
}

// Init starts the default pool. Call once at server startup, after idb.Init.
func Init(workers int, pythonExe, backendDir string, maxRetries int, store Store) {
	ctx, cancel := context.WithCancel(context.Background())
	defaultPool = &Pool{
		workers:    workers,
		queue:      make(chan Job, workers*8),
		pythonExe:  pythonExe,
		backendDir: backendDir,
		maxRetries: maxRetries,
		store:      store,
		running:    make(map[string]*exec.Cmd),
		ctx:        ctx,
		cancel:     cancel,
	}
	for i := 0; i < workers; i++ {
		defaultPool.wg.Add(1)
		go defaultPool.loop(i)
	}

	// Periodic + startup flush of locally-cached results that couldn't be
	// persisted earlier (network outage) — recovers already-computed work
	// without redoing any OCR.
	go func() {
		flush := func() {
			res, err := RunSync(context.Background(), Job{TaskType: "flush_pending_results"})
			if err != nil {
				log.Printf("[localqueue] pending-result flush failed: %v", err)
				return
			}
			if m, ok := res.Result.(map[string]any); ok {
				if f, _ := m["flushed"].(float64); f > 0 {
					log.Printf("[localqueue] recovered %d previously-uncommitted result(s)", int(f))
				}
			}
		}
		flush() // once at startup
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-defaultPool.ctx.Done():
				return
			case <-ticker.C:
				flush()
			}
		}
	}()

	log.Printf("[localqueue] pool started | workers=%d | python=%s | backendDir=%s", workers, pythonExe, backendDir)

}

// Stop signals all workers to finish their current job and exit, then waits.
// Call from main.go's graceful shutdown, alongside srv.Shutdown().
func Stop() {
	if defaultPool == nil {
		return
	}
	defaultPool.cancel()
	close(defaultPool.queue)
	defaultPool.wg.Wait()
}

// Submit enqueues a job. Safe to call from any goroutine (HTTP handlers).
func Submit(j Job) error {
	if defaultPool == nil {
		return fmt.Errorf("localqueue not initialized — call Init() at startup")
	}
	if j.ID == "" {
		if j.ExtractionID != "" {
			j.ID = j.ExtractionID
		} else {
			j.ID = j.CorrectionID
		}
	}
	select {
	case defaultPool.queue <- j:
		return nil
	default:
		return fmt.Errorf("localqueue full (%d workers, queue depth %d) — try again shortly", defaultPool.workers, cap(defaultPool.queue))
	}
}

func (p *Pool) loop(workerID int) {
	defer p.wg.Done()
	logger := log.New(os.Stderr, fmt.Sprintf("[localqueue-%d] ", workerID), log.LstdFlags)

	for {
		select {
		case <-p.ctx.Done():
			return
		case job, ok := <-p.queue:
			if !ok {
				return
			}
			p.runJob(logger, job)
		}
	}
}

func (p *Pool) runJob(logger *log.Logger, job Job) {
	logger.Printf("START job=%s type=%s retry=%d", job.ID, job.TaskType, job.RetryCount)

	payload, _ := json.Marshal(job)

	// PDF/OCR processing can take a while; 10 min ceiling per job so a
	// stuck job can't wedge a worker slot forever.
	jobCtx, cancel := context.WithTimeout(p.ctx, 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(jobCtx, p.pythonExe, "-m", "workers.cli")
	cmd.Dir = p.backendDir
	cmd.Env = os.Environ() // inherit DATABASE_URL, REDIS_HOST (unused now, harmless), EXPORT_DIR, etc.
	cmd.Stdin = bytes.NewReader(payload)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	p.mu.Lock()
	p.running[job.ID] = cmd
	p.mu.Unlock()

	err := cmd.Run()

	p.mu.Lock()
	delete(p.running, job.ID)
	p.mu.Unlock()

	if stderr := stderrBuf.String(); stderr != "" {
		logger.Printf("STDERR job=%s:\n%s", job.ID, stderr)
	}

	if err != nil {
		p.handleFailure(logger, job, fmt.Sprintf("subprocess error: %v", err))
		return
	}

	var res Result
	if jsonErr := json.Unmarshal(stdoutBuf.Bytes(), &res); jsonErr != nil {
		p.handleFailure(logger, job, fmt.Sprintf("invalid JSON from cli.py: %v (stdout: %q)", jsonErr, stdoutBuf.String()))
		return
	}

	if res.Status != "success" {
		p.handleFailure(logger, job, res.Error)
		return
	}

	logger.Printf("DONE job=%s", job.ID)
	// No DB write here — cli.py's task functions already committed the
	// final status/result_json/error themselves (same as the old Celery
	// tasks did). Go only needs to record FAILURE after retries are
	// exhausted, since a crashed/timed-out subprocess never got to write
	// its own failure row.
}

func (p *Pool) handleFailure(logger *log.Logger, job Job, reason string) {
	logger.Printf("FAIL job=%s reason=%s", job.ID, reason)

	if job.RetryCount >= p.maxRetries {
		logger.Printf("DROP job=%s retries exhausted (%d/%d)", job.ID, job.RetryCount, p.maxRetries)
		if p.store != nil && job.ExtractionID != "" {
			_ = p.store.MarkFailed(p.ctx, job.ExtractionID, reason)
		}
		return
	}

	job.RetryCount++
	backoff := time.Duration(job.RetryCount) * 30 * time.Second
	logger.Printf("RETRY job=%s in %v (attempt %d/%d)", job.ID, backoff, job.RetryCount, p.maxRetries)

	go func() {
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(backoff):
			_ = Submit(job)
		}
	}()
}
