// internal/oauthusers/store.go
//
// Minimal in-memory user/session store used by the Google OAuth handlers
// in routers/google_oauth.go. Swap this out for real queries against your
// Postgres pool (internal/db) when you're ready — the two functions below
// (FindOrCreate, Lookup) are the only surface the router relies on.
package oauthusers

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
)

// User is intentionally minimal — align field names with your real user
// table once this moves to Postgres.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Role     string `json:"role"`
}

var (
	mu           sync.RWMutex
	usersByEmail = map[string]*User{}

	sessionsMu sync.RWMutex
	sessions   = map[string]*User{} // opaque token -> user
)

// FindOrCreate looks up a user by the email Google returned, creating a new
// account if this is their first sign-in. Every account created this way
// gets the "analyst" role — the same default a normal RegisterPage
// submission produces.
func FindOrCreate(email, name string) (*User, error) {
	mu.Lock()
	defer mu.Unlock()

	if u, ok := usersByEmail[email]; ok {
		return u, nil
	}

	u := &User{
		ID:       randomID(),
		Username: usernameFromEmail(email, name),
		Email:    email,
		Role:     "analyst",
	}
	usersByEmail[email] = u
	return u, nil
}

// IssueSession mints an opaque session token for a user. Replace with a
// signed JWT / your existing session mechanism as needed.
func IssueSession(u *User) (string, error) {
	token := randomID()
	sessionsMu.Lock()
	sessions[token] = u
	sessionsMu.Unlock()
	return token, nil
}

// Lookup resolves a session token back to a user.
func Lookup(token string) (*User, bool) {
	sessionsMu.RLock()
	defer sessionsMu.RUnlock()
	u, ok := sessions[token]
	return u, ok
}

// RandomState returns a random token suitable for the OAuth CSRF-state
// cookie (also reused as the general-purpose ID generator below).
func RandomState() string {
	return randomID()
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func usernameFromEmail(email, name string) string {
	if name != "" {
		return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", "_"))
	}
	if at := strings.Index(email, "@"); at != -1 {
		return email[:at]
	}
	return email
}
