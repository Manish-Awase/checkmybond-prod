// internal/middleware/middleware.go
// Three middleware functions:
//   Identity  — reads gateway-injected headers into request context
//   AuditLog  — writes one row to audit_log table after every request
//   RequireRole — RBAC enforcement, reads role from context
package middleware

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	idb "hitl-platform/internal/db"
)

// ── Context keys ──────────────────────────────────────────────────────────────

type ctxKey string

const (
	KeyUserID    ctxKey = "user_id"
	KeyUserRole  ctxKey = "user_role"
	KeyTokenExp  ctxKey = "token_exp"
	KeyTokenHash ctxKey = "token_hash"
)

func UserID(r *http.Request) string {
	v, _ := r.Context().Value(KeyUserID).(string)
	return v
}

func UserRole(r *http.Request) string {
	v, _ := r.Context().Value(KeyUserRole).(string)
	return v
}

func TokenExp(r *http.Request) string {
	v, _ := r.Context().Value(KeyTokenExp).(string)
	return v
}

func TokenHash(r *http.Request) string {
	v, _ := r.Context().Value(KeyTokenHash).(string)
	return v
}

// ── Identity middleware ───────────────────────────────────────────────────────
// Reads the four headers injected by the gateway after JWT verification.
// Rejects requests that arrive without them (direct :8000 access).

func Identity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID   := r.Header.Get("X-User-Id")
		userRole := r.Header.Get("X-User-Role")

		if userID == "" || userRole == "" {
			http.Error(w,
				`{"detail":"Missing gateway identity headers. Use port 9000."}`,
				http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), KeyUserID,    userID)
		ctx  = context.WithValue(ctx,          KeyUserRole, userRole)
		ctx  = context.WithValue(ctx,          KeyTokenExp,  r.Header.Get("X-Token-Exp"))
		ctx  = context.WithValue(ctx,          KeyTokenHash, r.Header.Get("X-Token-Hash"))

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ── RBAC middleware ───────────────────────────────────────────────────────────

func RequireRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[strings.ToLower(r)] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := strings.ToLower(UserRole(r))
			if _, ok := allowed[role]; !ok {
				http.Error(w,
					fmt.Sprintf(`{"detail":"Role '%s' cannot access this endpoint"}`, role),
					http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ── Audit log middleware ──────────────────────────────────────────────────────
// Reads body, passes it downstream unchanged, writes AuditLog row after response.

func AuditLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Read + re-attach body so downstream still gets it
		var bodyHash string
		if r.Body != nil {
			bodyBytes, err := io.ReadAll(r.Body)
			if err == nil {
				h := sha256.Sum256(bodyBytes)
				bodyHash = fmt.Sprintf("%x", h[:8]) // 16 hex chars
				r.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
			}
		}

		// Wrap ResponseWriter to capture status code
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		durationMS := int(time.Since(start).Milliseconds())
		userID     := r.Header.Get("X-User-Id")
		if userID == "" {
			userID = "anonymous"
		}
		query := r.URL.RawQuery

		log := &idb.AuditLog{
			Path:       r.URL.Path,
			Method:     r.Method,
			UserID:     userID,
			DurationMS: durationMS,
			StatusCode: rw.status,
			BodyHash:   bodyHash,
		}
		if query != "" {
			log.QueryParams = &query
		}

		// Fire and forget — audit failure must never break the request
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = idb.WriteAuditLog(ctx, log)
		}()
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}
