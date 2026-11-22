// routers/contact.go
//
// Public, unauthenticated endpoint for the marketing site's contact
// form. No imw.Identity middleware -- anonymous visitors need to reach
// this. That makes it a spam/abuse target on its own; the honeypot
// field below helps, but this endpoint ALSO needs its own nginx rate
// limit (a new, loose zone -- reuse the existing "auth" zone pattern,
// don't skip rate limiting just because it's not the login endpoint).
package routers

import (
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	iemail "hitl-platform/internal/email"
)

const (
	contactMaxNameLen    = 200
	contactMaxEmailLen   = 320
	contactMaxMessageLen = 5000
)

func ContactRouter() http.Handler {
	r := chi.NewRouter()
	r.Post("/", handleContactSubmit)
	return r
}

func handleContactSubmit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Message string `json:"message"`
		Website string `json:"website"` // honeypot -- real users never fill this
	}
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Honeypot tripped -- silently pretend success so the bot doesn't
	// learn to adapt; don't send an email, don't log it as an error.
	if strings.TrimSpace(req.Website) != "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "sent"})
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(req.Email)
	req.Message = strings.TrimSpace(req.Message)

	if req.Name == "" || req.Email == "" || req.Message == "" {
		errJSON(w, http.StatusBadRequest, "name, email, and message are required")
		return
	}
	if len(req.Name) > contactMaxNameLen || len(req.Email) > contactMaxEmailLen || len(req.Message) > contactMaxMessageLen {
		errJSON(w, http.StatusBadRequest, "one or more fields exceed the maximum allowed length")
		return
	}
	if !strings.Contains(req.Email, "@") {
		errJSON(w, http.StatusBadRequest, "please provide a valid email address")
		return
	}

	destEmail := os.Getenv("SUPPORT_EMAIL")
	if destEmail == "" {
		destEmail = "support@example.com" // set SUPPORT_EMAIL before deploying
	}
	iemail.SendContactSubmission(destEmail, req.Name, req.Email, req.Message)

	writeJSON(w, http.StatusOK, map[string]any{"status": "sent"})
}
