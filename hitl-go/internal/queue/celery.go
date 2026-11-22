// internal/queue/celery.go
// Enqueues tasks to Redis in the exact Celery wire format so Python
// workers pick them up without any changes.
//
// Celery message format (JSON protocol 2):
//
//	LPUSH celery <json>
//
// where <json> is a serialised CeleryMessage with a base64-encoded body.
package queue

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var rdb *redis.Client

func Init() {
	rdb = redis.NewClient(&redis.Options{
		Addr: envOr("REDIS_HOST", "localhost") + ":6379",
		DB:   1, // Celery broker on DB 1 — matches celery_app.py
	})
}

func Close() {
	if rdb != nil {
		rdb.Close()
	}
}

// Ping confirms Redis is reachable via the same client used to enqueue
// Celery tasks (DB 1). Doesn't separately check the blacklist client's
// connection (DB 0, in the gateway process, not here) — same Redis
// instance, so if this succeeds that one almost certainly does too.
func Ping(ctx context.Context) error {
	if rdb == nil {
		return fmt.Errorf("redis client not initialized")
	}
	return rdb.Ping(ctx).Err()
}

// ── Celery wire format ────────────────────────────────────────────────────────

type celeryMessage struct {
	Body        string           `json:"body"`
	ContentType string           `json:"content-type"`
	Encoding    string           `json:"content-encoding"`
	Headers     celeryHeaders    `json:"headers"`
	Properties  celeryProperties `json:"properties"`
}

type celeryHeaders struct {
	Lang          string  `json:"lang"`
	Task          string  `json:"task"`
	ID            string  `json:"id"`
	RootID        string  `json:"root_id"`
	ParentID      string  `json:"parent_id"`
	Group         string  `json:"group"`
	Meth          string  `json:"meth"`
	Shadow        string  `json:"shadow"`
	ETA           *string `json:"eta"`
	Expires       *string `json:"expires"`
	Retries       int     `json:"retries"`
	TimeLimitSoft int     `json:"timelimit"`
	TimeLimitHard int     `json:"timelimit_hard"`
	ArgsRepr      string  `json:"argsrepr"`
	KwargsRepr    string  `json:"kwargsrepr"`
	Origin        string  `json:"origin"`
	Ignore        bool    `json:"ignore_result"`
}

type celeryProperties struct {
	CorrelationID string         `json:"correlation_id"`
	ReplyTo       string         `json:"reply_to"`
	DeliveryMode  int            `json:"delivery_mode"`
	DeliveryInfo  celeryDelivery `json:"delivery_info"`
	Priority      int            `json:"priority"`
	BodyEncoding  string         `json:"body_encoding"`
	DeliveryTag   string         `json:"delivery_tag"`
}

type celeryDelivery struct {
	Exchange   string `json:"exchange"`
	RoutingKey string `json:"routing_key"`
}

// EnqueueExtractionJob pushes a process_document task to the Celery broker.
// Python's celery_app.process_document receives (extraction_id, pdf_path, schema_id, doc_hash).
func EnqueueExtractionJob(ctx context.Context, extractionID, pdfPath, schemaID, docHash string) error {
	taskID := uuid.New().String()

	// Celery body = base64( json([args, kwargs, embed]) )
	// args order must match Python task signature exactly
	args := []any{extractionID, pdfPath, schemaID, docHash}
	kwargs := map[string]any{}
	embed := map[string]any{"callbacks": nil, "errbacks": nil, "chain": nil, "chord": nil}
	bodyRaw, err := json.Marshal([]any{args, kwargs, embed})
	if err != nil {
		return fmt.Errorf("marshal celery body: %w", err)
	}
	body := base64.StdEncoding.EncodeToString(bodyRaw)

	msg := celeryMessage{
		Body:        body,
		ContentType: "application/json",
		Encoding:    "utf-8",
		Headers: celeryHeaders{
			Lang:       "py",
			Task:       "workers.celery_app.process_document",
			ID:         taskID,
			RootID:     taskID,
			ParentID:   "",
			Group:      "",
			Retries:    0,
			ArgsRepr:   fmt.Sprintf("(%q, ...)", extractionID),
			KwargsRepr: "{}",
			Origin:     "go-server",
		},
		Properties: celeryProperties{
			CorrelationID: taskID,
			ReplyTo:       uuid.New().String(),
			DeliveryMode:  2, // persistent
			DeliveryInfo: celeryDelivery{
				Exchange:   "",
				RoutingKey: "celery",
			},
			Priority:     0,
			BodyEncoding: "base64",
			DeliveryTag:  uuid.New().String(),
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal celery message: %w", err)
	}

	return rdb.LPush(ctx, "celery", string(data)).Err()
}

// EnqueueSingleOCRDocument pushes a process_single_ocr_document task to Celery.
// serialNumber is 1-based and used to name the output file so folder batches
// don't overwrite each other (e.g. 1_invoice.pdf, 2_invoice.pdf, ...).
func EnqueueSingleOCRDocument(ctx context.Context, extractionID, pdfPath, schemaID, docHash string, regionsJSON string, serialNumber int) error {
	taskID := uuid.New().String()

	args := []any{extractionID, pdfPath, schemaID, docHash, regionsJSON, serialNumber}
	kwargs := map[string]any{}
	embed := map[string]any{"callbacks": nil, "errbacks": nil, "chain": nil, "chord": nil}
	bodyRaw, err := json.Marshal([]any{args, kwargs, embed})
	if err != nil {
		return fmt.Errorf("marshal celery body: %w", err)
	}
	body := base64.StdEncoding.EncodeToString(bodyRaw)

	msg := celeryMessage{
		Body:        body,
		ContentType: "application/json",
		Encoding:    "utf-8",
		Headers: celeryHeaders{
			Lang:       "py",
			Task:       "workers.celery_app.process_single_ocr_document",
			ID:         taskID,
			RootID:     taskID,
			ParentID:   "",
			Group:      "",
			Retries:    0,
			ArgsRepr:   fmt.Sprintf("(%q, ...)", extractionID),
			KwargsRepr: "{}",
			Origin:     "go-server",
		},
		Properties: celeryProperties{
			CorrelationID: taskID,
			ReplyTo:       uuid.New().String(),
			DeliveryMode:  2,
			DeliveryInfo: celeryDelivery{
				Exchange:   "",
				RoutingKey: "celery",
			},
			Priority:     0,
			BodyEncoding: "base64",
			DeliveryTag:  uuid.New().String(),
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal celery message: %w", err)
	}

	return rdb.LPush(ctx, "celery", string(data)).Err()
}

// // EnqueueSingleOCRDocument pushes a process_single_ocr_document task to Celery.
// // This is used for the single-PDF direct-correction flow (no queue/review step).
// // The regionsJSON argument is the raw JSON array from the frontend so the worker
// // knows both the coordinates and the user-supplied correction values.
// func EnqueueSingleOCRDocument(ctx context.Context, extractionID, pdfPath, schemaID, docHash string, regionsJSON string) error {
// 	taskID := uuid.New().String()

// 	args := []any{extractionID, pdfPath, schemaID, docHash, regionsJSON}
// 	kwargs := map[string]any{}
// 	embed := map[string]any{"callbacks": nil, "errbacks": nil, "chain": nil, "chord": nil}
// 	bodyRaw, err := json.Marshal([]any{args, kwargs, embed})
// 	if err != nil {
// 		return fmt.Errorf("marshal celery body: %w", err)
// 	}
// 	body := base64.StdEncoding.EncodeToString(bodyRaw)

// 	msg := celeryMessage{
// 		Body:        body,
// 		ContentType: "application/json",
// 		Encoding:    "utf-8",
// 		Headers: celeryHeaders{
// 			Lang:       "py",
// 			Task:       "workers.celery_app.process_single_ocr_document",
// 			ID:         taskID,
// 			RootID:     taskID,
// 			ParentID:   "",
// 			Group:      "",
// 			Retries:    0,
// 			ArgsRepr:   fmt.Sprintf("(%q, ...)", extractionID),
// 			KwargsRepr: "{}",
// 			Origin:     "go-server",
// 		},
// 		Properties: celeryProperties{
// 			CorrelationID: taskID,
// 			ReplyTo:       uuid.New().String(),
// 			DeliveryMode:  2,
// 			DeliveryInfo: celeryDelivery{
// 				Exchange:   "",
// 				RoutingKey: "celery",
// 			},
// 			Priority:     0,
// 			BodyEncoding: "base64",
// 			DeliveryTag:  uuid.New().String(),
// 		},
// 	}

// 	data, err := json.Marshal(msg)
// 	if err != nil {
// 		return fmt.Errorf("marshal celery message: %w", err)
// 	}

// 	return rdb.LPush(ctx, "celery", string(data)).Err()
// }

// EnqueueBondTask pushes an arbitrary named Celery task with positional
// args, for cases (like preflight_fingerprint) where a dedicated
// EnqueueXxx wrapper doesn't exist yet. Prefer a dedicated wrapper
// (see EnqueueExtractionJob, EnqueueParsedContentJob) once a task's
// call site stabilizes — this one exists because
// routers/templates_preflight.go calls it directly with a task name
// string rather than through its own wrapper.
func EnqueueBondTask(ctx context.Context, taskName string, args ...any) error {
	taskID := uuid.New().String()

	kwargs := map[string]any{}
	embed := map[string]any{"callbacks": nil, "errbacks": nil, "chain": nil, "chord": nil}
	bodyRaw, err := json.Marshal([]any{args, kwargs, embed})
	if err != nil {
		return fmt.Errorf("marshal celery body: %w", err)
	}
	body := base64.StdEncoding.EncodeToString(bodyRaw)

	msg := celeryMessage{
		Body:        body,
		ContentType: "application/json",
		Encoding:    "utf-8",
		Headers: celeryHeaders{
			Lang:       "py",
			Task:       taskName,
			ID:         taskID,
			RootID:     taskID,
			Retries:    0,
			ArgsRepr:   fmt.Sprintf("(%v, ...)", args),
			KwargsRepr: "{}",
			Origin:     "go-server",
		},
		Properties: celeryProperties{
			CorrelationID: taskID,
			ReplyTo:       uuid.New().String(),
			DeliveryMode:  2,
			DeliveryInfo: celeryDelivery{
				Exchange:   "",
				RoutingKey: "celery",
			},
			Priority:     0,
			BodyEncoding: "base64",
			DeliveryTag:  uuid.New().String(),
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal celery message: %w", err)
	}
	return rdb.LPush(ctx, "celery", string(data)).Err()
}

// ── Blacklist helpers (used by gateway and account router) ───────────────────

var rdbBlacklist *redis.Client

// BlacklistClient exposes the shared DB-0 client for other packages that
// need a Redis connection to the same database (e.g.
// internal/redisclient, used by loginattempts/otp) instead of each
// opening their own separate pool to the identical Redis instance.
func BlacklistClient() *redis.Client {
	return rdbBlacklist
}

func InitBlacklist() {
	rdbBlacklist = redis.NewClient(&redis.Options{
		Addr: envOr("REDIS_HOST", "localhost") + ":6379",
		DB:   0, // blacklist on DB 0 — matches redis_blacklist.py
	})
}

func BlacklistToken(ctx context.Context, tokenHash, userID string, ttlSeconds int64) error {
	key := "blacklist:token:" + tokenHash
	return rdbBlacklist.SetEx(ctx, key, "1", time.Duration(ttlSeconds)*time.Second).Err()
}

func BlacklistUserFlag(ctx context.Context, userID string) error {
	key := "blacklist:user_flag:" + userID
	// 24h TTL — covers max JWT lifetime
	return rdbBlacklist.SetEx(ctx, key, "1", 24*time.Hour).Err()
}

func RemoveUserBlacklist(ctx context.Context, userID string) error {
	// Remove user flag
	flagKey := "blacklist:user_flag:" + userID
	// Remove all token keys stored in the user set
	setKey := "blacklist:user:" + userID
	members, err := rdbBlacklist.SMembers(ctx, setKey).Result()
	if err != nil && err != redis.Nil {
		return err
	}
	keys := append(members, flagKey, setKey)
	return rdbBlacklist.Del(ctx, keys...).Err()
}

func IsBlacklisted(ctx context.Context, tokenHash, userID string) (bool, error) {
	tokenKey := "blacklist:token:" + tokenHash
	flagKey := "blacklist:user_flag:" + userID
	n, err := rdbBlacklist.Exists(ctx, tokenKey, flagKey).Result()
	return n > 0, err
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── PDF Correction Task ───────────────────────────────────────────────────────

// PDFCorrectionArgs holds all arguments needed by the Python
// apply_correction_to_pdf Celery task.
type PDFCorrectionArgs struct {
	CorrectionID     string `json:"correction_id"`
	ExtractionID     string `json:"extraction_id"`
	PdfPath          string `json:"pdf_path"`
	FieldName        string `json:"field_name"`
	OriginalValue    string `json:"original_value"`
	CorrectedValue   string `json:"corrected_value"`
	SerialNumber     int    `json:"serial_number"`
	OriginalFilename string `json:"original_filename"`
}

// EnqueuePDFCorrectionTask pushes a apply_correction_to_pdf task to Celery.
// The task is picked up by the Python worker which uses PyMuPDF to apply
// the text correction directly to the PDF and exports the result to
// corrected_pdf/pdf/{serial}_{filename}.
func EnqueuePDFCorrectionTask(ctx context.Context, args PDFCorrectionArgs) error {
	taskID := uuid.New().String()

	// Build positional args list matching the Python function signature:
	// apply_correction_to_pdf(self, correction_id, extraction_id, pdf_path,
	//   field_name, original_value, corrected_value, serial_number, original_filename)
	body := map[string]any{
		"args": []any{
			args.CorrectionID,
			args.ExtractionID,
			args.PdfPath,
			args.FieldName,
			args.OriginalValue,
			args.CorrectedValue,
			args.SerialNumber,
			args.OriginalFilename,
		},
		"kwargs":    map[string]any{},
		"callbacks": nil,
		"errbacks":  nil,
		"chord":     nil,
		"chain":     nil,
	}

	bodyRaw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	bodyB64 := base64.StdEncoding.EncodeToString(bodyRaw)

	msg := celeryMessage{
		Body:        bodyB64,
		ContentType: "application/json",
		Encoding:    "utf-8",
		Headers: celeryHeaders{
			Lang:       "py",
			Task:       "workers.celery_app.apply_correction_to_pdf",
			ID:         taskID,
			RootID:     taskID,
			ParentID:   "",
			Group:      "",
			Retries:    0,
			ArgsRepr:   fmt.Sprintf("(%q, ...)", args.CorrectionID),
			KwargsRepr: "{}",
			Origin:     "go-hitl-server",
			ETA:        nil,
			Expires:    nil,
		},
		Properties: celeryProperties{
			CorrelationID: taskID,
			ReplyTo:       "",
			DeliveryMode:  2,
			DeliveryTag:   taskID,
			DeliveryInfo: celeryDelivery{
				Exchange:   "",
				RoutingKey: "celery",
			},
			Priority:     0,
			BodyEncoding: "base64",
		},
	}

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	return rdb.LPush(ctx, "celery", string(msgBytes)).Err()
}
