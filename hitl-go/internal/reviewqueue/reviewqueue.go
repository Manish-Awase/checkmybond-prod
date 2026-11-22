// hitl-go/internal/reviewqueue/reviewqueue.go
//
// Removes entries from the SLA/HITL review queue directly via Redis —
// no Python subprocess call. This matters for the website deployment
// specifically: Go and Python run in SEPARATE Docker containers there,
// so Go cannot exec a Python process the way it can on desktop (same
// machine, same filesystem). Since the review queue is just a Redis
// sorted set (backend/hitl/sla_queue.py's SLAQueue — same key, same DB,
// same JSON item format), Go can manipulate it directly instead, which
// works identically for both deployments.
//
// Deliberately its own tiny package, not folded into internal/queue
// (the legacy Celery-broker package, DB 1/2, still actively used by the
// website) or internal/localqueue (desktop-only, doesn't exist on the
// website at all) — this way it's a clean drop-in for either codebase.
package reviewqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/redis/go-redis/v9"
)

const queueKey = "hitl:review_queue" // must match backend/hitl/sla_queue.py's QUEUE_KEY exactly

var rdb *redis.Client

// Init connects to the same Redis DB the Python SLAQueue uses (DB 0 —
// distinct from internal/queue's Celery broker on DB 1). Call once at
// server startup, same pattern as queue.Init()/localqueue.Init().
func Init() {
	rdb = redis.NewClient(&redis.Options{
		Addr: envOr("REDIS_HOST", "localhost") + ":6379",
		DB:   0,
	})
}

func Close() {
	if rdb != nil {
		rdb.Close()
	}
}

// RemoveByExtractionIDs removes every review-queue entry whose
// extraction_id is in the given set, and returns how many were removed.
// No secondary index by extraction_id exists (items are just JSON blobs
// in a sorted set), so this scans the whole queue — fine at review-queue
// scale (tens/hundreds of pending items, not millions).
func RemoveByExtractionIDs(ctx context.Context, extractionIDs []string) (int, error) {
	if rdb == nil {
		return 0, fmt.Errorf("reviewqueue not initialized — call Init() at startup")
	}
	if len(extractionIDs) == 0 {
		return 0, nil
	}

	wanted := make(map[string]struct{}, len(extractionIDs))
	for _, id := range extractionIDs {
		wanted[id] = struct{}{}
	}

	members, err := rdb.ZRange(ctx, queueKey, 0, -1).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to read review queue: %w", err)
	}

	removed := 0
	for _, raw := range members {
		var item struct {
			ExtractionID string `json:"extraction_id"`
		}
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			continue // skip malformed entries rather than fail the whole operation
		}
		if _, ok := wanted[item.ExtractionID]; !ok {
			continue
		}
		if n, err := rdb.ZRem(ctx, queueKey, raw).Result(); err == nil && n > 0 {
			removed++
		}
	}
	return removed, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
