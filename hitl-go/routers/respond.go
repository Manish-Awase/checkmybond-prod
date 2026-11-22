// routers/respond.go
package routers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"

	imetrics "hitl-platform/internal/metrics"
)

type jsonError struct {
	Error string `json:"error"`
}

// writeJSONError writes a {"error": "..."} body with the given status.
// Shared by any endpoint file in this package.
//
// Use this directly for messages that are SAFE to show a user verbatim:
// validation failures ("quantity must be at least 1"), business-rule
// rejections ("that coupon has already been used"), and anything else
// built entirely from fixed strings or values the user themself
// supplied. Never pass err.Error() from a database driver, an HTTP
// client, or any other infrastructure-level error here — those can leak
// internals (connection details, query fragments, library internals,
// stack-shaped text) and belong in writeInternalError instead.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(jsonError{Error: msg})
}

// defaultInternalMessage is what a caller of writeInternalError gets when
// it doesn't supply its own public-facing text.
const defaultInternalMessage = "internal problem — please try again"

// writeInternalError is for the other kind of error: one that came from
// something outside the request itself — a database call, a payment
// gateway, a network timeout, anything unplanned. The real error goes to
// the server log (with the request's trace id, so it's findable from
// there), and the client gets a fixed, generic message that reveals
// nothing about the failure.
//
// publicMsg is optional — pass "" to use defaultInternalMessage
// ("internal problem — please try again"). A caller that wants a more
// specific but still-generic message for its context (e.g. "payment
// could not be completed, please try again or use a different payment
// method") can supply one, as long as it's a fixed string and not
// anything derived from err.
//
// Usage:
//
//	if err != nil {
//	    writeInternalError(w, r, http.StatusInternalServerError, err, "")
//	    return
//	}
func writeInternalError(w http.ResponseWriter, r *http.Request, status int, err error, publicMsg string) {
	imetrics.LogCtx(r.Context(), "error", "internal_error", map[string]any{
		"path":   r.URL.Path,
		"method": r.Method,
		"error":  err.Error(),
	})
	if publicMsg == "" {
		publicMsg = defaultInternalMessage
	}
	writeJSONError(w, status, publicMsg)
}

// newID returns a random hex id, used for transaction ids and anything
// else in this package that needs an opaque unique string.
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
