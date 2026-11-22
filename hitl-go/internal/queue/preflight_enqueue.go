// internal/queue/preflight_enqueue.go
package queue

import (
	"context"
	"fmt"
	"time"
)

// SetPreflightResult / GetPreflightResult use rdbBlacklist (Redis DB 0),
// NOT rdb (DB 1, the Celery broker — see celery.go's Init()). The Python
// preflight_fingerprint task writes its result to
// redis://{host}:6379/0 explicitly, so this side has to read the same
// DB or every preflight will look permanently stuck at "processing".
// rdbBlacklist is already a DB-0 client (see InitBlacklist() in
// celery.go) — reused here rather than opening a third connection.
// Requires InitBlacklist() to have been called at startup, same as the
// existing blacklist functions in this package.

// SetPreflightResult caches preflight result in Redis (TTL 10 min).
func SetPreflightResult(ctx context.Context, preflightID string, resultJSON string) error {
	if rdbBlacklist == nil {
		return fmt.Errorf("redis (blacklist/DB0) client not initialized")
	}
	key := fmt.Sprintf("preflight:%s", preflightID)
	return rdbBlacklist.Set(ctx, key, resultJSON, 10*time.Minute).Err()
}

// GetPreflightResult retrieves cached preflight result.
func GetPreflightResult(ctx context.Context, preflightID string) (string, error) {
	if rdbBlacklist == nil {
		return "", fmt.Errorf("redis (blacklist/DB0) client not initialized")
	}
	key := fmt.Sprintf("preflight:%s", preflightID)
	return rdbBlacklist.Get(ctx, key).Result()
}
