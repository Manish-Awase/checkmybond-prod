// internal/redisclient/redisclient.go
//
// Shared Redis client for loginattempts and otp. Points at the SAME
// underlying DB-0 client internal/queue already maintains for token
// blacklisting (queue.InitBlacklist()/BlacklistClient()), rather than
// opening a second connection pool to the identical Redis database --
// there is no reason for this process to hold two separate pools
// against the same instance and DB index.
//
// Requires queue.InitBlacklist() to have already been called at startup
// (it already is, in cmd/server/main.go, for the existing blacklist
// feature) before anything in this package is used.
package redisclient

import (
	"github.com/redis/go-redis/v9"

	iqueue "hitl-platform/internal/queue"
)

// Client returns the shared Redis (DB 0) client used for login-attempt
// tracking and OTP codes.
func Client() *redis.Client {
	return iqueue.BlacklistClient()
}
