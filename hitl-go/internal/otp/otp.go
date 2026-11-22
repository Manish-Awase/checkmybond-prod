// internal/otp/otp.go
//
// Step-up email OTP for logins that have crossed otpFailThreshold failed
// attempts (see routers/auth.go). Codes are 6-digit numeric, hashed
// before storage (defense in depth -- a Redis compromise or a stray log
// line shouldn't hand out live codes), expire in 5 minutes, and allow a
// limited number of guesses before being locked out until a fresh code
// is requested.
package otp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/redis/go-redis/v9"

	iredis "hitl-platform/internal/redisclient"
)

const (
	codeTTL         = 5 * time.Minute
	maxVerifyTries  = 5
	codeDigitBound  = 1000000 // 6 digits: 000000–999999
)

// ErrAlreadyOutstanding means a code was already issued for this
// identifier and hasn't expired yet -- callers should NOT send another
// email, just tell the user to check their inbox for the existing one.
// This is what keeps repeated login submissions (without an otp_code)
// from spamming the user's inbox with a fresh code every time.
var ErrAlreadyOutstanding = errors.New("otp already outstanding for this identifier")

func codeKey(identifier string) string     { return "login_otp:" + identifier }
func attemptsKey(identifier string) string { return "login_otp_attempts:" + identifier }

func hash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// GenerateCode returns a cryptographically random 6-digit numeric code,
// zero-padded (e.g. "004821").
func GenerateCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(codeDigitBound))
	if err != nil {
		return "", fmt.Errorf("generate otp code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// Store records the (hashed) code for this identifier, IF one isn't
// already outstanding. Returns ErrAlreadyOutstanding if a still-valid
// code already exists -- the caller should treat that as "don't resend
// email" rather than as a real error.
func Store(ctx context.Context, identifier, code string) error {
	c := iredis.Client()
	key := codeKey(identifier)

	ok, err := c.SetNX(ctx, key, hash(code), codeTTL).Result()
	if err != nil {
		return fmt.Errorf("store otp code: %w", err)
	}
	if !ok {
		return ErrAlreadyOutstanding
	}

	// Fresh code means a fresh attempts budget.
	if err := c.Del(ctx, attemptsKey(identifier)).Err(); err != nil {
		return fmt.Errorf("reset otp attempts: %w", err)
	}
	return nil
}

// Verify checks a submitted code against the stored hash. Guessing is
// capped at maxVerifyTries per outstanding code -- once exceeded, further
// calls return (false, nil) without even checking the hash, until the
// code expires or a new one is requested via Store.
func Verify(ctx context.Context, identifier, code string) (bool, error) {
	c := iredis.Client()

	tries, err := c.Incr(ctx, attemptsKey(identifier)).Result()
	if err != nil {
		return false, fmt.Errorf("increment otp attempts: %w", err)
	}
	// Keep the attempts counter alive roughly as long as the code itself.
	c.Expire(ctx, attemptsKey(identifier), codeTTL)

	if tries > maxVerifyTries {
		return false, nil
	}

	stored, err := c.Get(ctx, codeKey(identifier)).Result()
	if err == redis.Nil {
		return false, nil // expired or never issued
	}
	if err != nil {
		return false, fmt.Errorf("get otp code: %w", err)
	}

	if stored != hash(code) {
		return false, nil
	}

	// Success — burn the code so it can't be replayed, and clear attempts.
	c.Del(ctx, codeKey(identifier), attemptsKey(identifier))
	return true, nil
}
