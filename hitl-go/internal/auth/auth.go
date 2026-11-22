// internal/auth/auth.go
//
// SECURITY REDESIGN: switched from HS256 (symmetric — same secret signs
// AND verifies) to RS256 (asymmetric — private key signs, public key
// verifies). This matters specifically because the OLD JWT_SECRET had to
// ship inside every desktop install just so gateway/hitl-server could
// verify incoming tokens — which meant anyone extracting it from their
// own copy could forge a brand-new, validly-signed JWT claiming to be
// ANY user (including admin), no real credentials needed at all.
//
// With RS256: only the new cmd/authservice binary (server-only, never
// bundled into the Tauri installer) ever loads the PRIVATE key and calls
// CreateAccessToken. Every desktop install only needs the PUBLIC key to
// call ParseToken — and a public key can verify signatures but cannot
// forge new ones, so shipping it is safe by design, not just by policy.
package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const expireMinutes = 60

// ── Password (unchanged — bcrypt was already correct) ──────────────────────

func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

func VerifyPassword(plain, hashed string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hashed), []byte(plain)) == nil
}

// ── Key loading ──────────────────────────────────────────────────────────────
// Deliberately lazy, not loaded at package-init time: a binary that only
// ever calls ParseToken (gateway, hitl-server) should never even attempt
// to read a private key file that, by design, doesn't exist on that
// machine at all.

var (
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
)

// loadPrivateKey reads the RSA private key — only ever called from
// cmd/authservice, which is the ONLY binary that should have this file
// present. Fails loudly (not a silent fallback) if missing, since a
// silent fallback here is exactly the kind of thing that could
// accidentally ship a weakened auth path.
func loadPrivateKey() (*rsa.PrivateKey, error) {
	if privateKey != nil {
		return privateKey, nil
	}
	path := os.Getenv("JWT_PRIVATE_KEY_PATH")
	if path == "" {
		return nil, fmt.Errorf("JWT_PRIVATE_KEY_PATH not set — this binary should not be signing tokens")
	}
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read private key at %s: %w", path, err)
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("could not parse private key: %w", err)
	}
	privateKey = key
	return privateKey, nil
}

// loadPublicKey reads the RSA public key — safe to call from any binary,
// including every desktop install. The key itself is safe to ship;
// PUBLIC_KEY_PATH just needs to point at wherever it's bundled.
func loadPublicKey() (*rsa.PublicKey, error) {
	if publicKey != nil {
		return publicKey, nil
	}
	path := os.Getenv("JWT_PUBLIC_KEY_PATH")
	if path == "" {
		return nil, fmt.Errorf("JWT_PUBLIC_KEY_PATH not set")
	}
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read public key at %s: %w", path, err)
	}
	key, err := jwt.ParseRSAPublicKeyFromPEM(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("could not parse public key: %w", err)
	}
	publicKey = key
	return publicKey, nil
}

// ── JWT ───────────────────────────────────────────────────────────────────────

type Claims struct {
	Sub      string `json:"sub"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// CreateAccessToken — ONLY ever called by cmd/authservice. If this is
// somehow called from a binary without JWT_PRIVATE_KEY_PATH set (i.e.
// any desktop install), it fails cleanly rather than falling back to
// anything weaker.
func CreateAccessToken(userID, username, role string) (string, error) {
	key, err := loadPrivateKey()
	if err != nil {
		return "", fmt.Errorf("cannot create tokens on this binary: %w", err)
	}

	claims := Claims{
		Sub:      userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().UTC().Add(
				time.Duration(expireMinutes) * time.Minute)),
			IssuedAt: jwt.NewNumericDate(time.Now().UTC()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(key)
}

// ParseToken — safe to call from any binary, including every desktop
// install, since it only ever needs the public key.
func ParseToken(tokenStr string) (*Claims, error) {
	key, err := loadPublicKey()
	if err != nil {
		return nil, fmt.Errorf("cannot verify tokens: %w", err)
	}

	token, err := jwt.ParseWithClaims(tokenStr, &Claims{},
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return key, nil
		})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

// ── Password Reset Tokens ─────────────────────────────────────────────────────
// Add these imports to the top of internal/auth/auth.go:
//   "crypto/rand"
//   "crypto/sha256"
//   "encoding/hex"

// GenerateResetToken creates a cryptographically secure random token.
// Returns the raw token (for the email link) and its SHA-256 hash (for DB storage).
func GenerateResetToken() (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate reset token: %w", err)
	}
	raw = hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(sum[:])
	return raw, hash, nil
}

// HashResetToken hashes a raw token for DB lookup.
func HashResetToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
