// internal/loginattempts/loginattempts.go
//
// Tracks failed login attempts per identifier (whatever the client typed
// as `login` -- username or email) in Redis, so handleLogin can decide
// when to start requiring a CAPTCHA (captchaFailThreshold) and, at a
// higher count, an OTP step-up (otpFailThreshold) -- see routers/auth.go.
package loginattempts

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	iredis "hitl-platform/internal/redisclient"
)

// window is how long a failed-attempt count is remembered before it
// resets on its own. 15 minutes matches the existing password-reset
// token TTL elsewhere in this codebase.
const window = 15 * time.Minute

func keyFor(identifier string) string {
	return "login_fail:" + identifier
}

// RecordFailure increments the failure count for this identifier and
// (re)sets its TTL to `window`, so the count only ever reflects failures
// within the last `window` -- not an all-time total. Returns the new
// count.
func RecordFailure(ctx context.Context, identifier string) (int64, error) {
	c := iredis.Client()
	key := keyFor(identifier)

	pipe := c.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return incr.Val(), nil
}

// Count returns the current failure count for this identifier (0 if none
// recorded, or if the window already expired).
func Count(ctx context.Context, identifier string) (int64, error) {
	c := iredis.Client()
	val, err := c.Get(ctx, keyFor(identifier)).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return val, nil
}

// Reset clears the failure count -- called once a login fully succeeds
// (password + CAPTCHA if required + OTP if required), so a legitimate
// user isn't stuck facing extra steps on a later, unrelated login after
// the window naturally would have cleared it.
func Reset(ctx context.Context, identifier string) error {
	c := iredis.Client()
	return c.Del(ctx, keyFor(identifier)).Err()
}
