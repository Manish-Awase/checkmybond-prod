// routers/google_oauth.go
package routers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"hitl-platform/internal/oauthusers"
)

var googleOAuthConfig = &oauth2.Config{
	ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
	ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
	RedirectURL:  envOr("GOOGLE_REDIRECT_URL", "http://localhost:8000/api/auth/oauth/google/callback"),
	Scopes:       []string{"openid", "email", "profile"},
	Endpoint:     google.Endpoint,
}

var frontendURL = envOr("FRONTEND_URL", "http://localhost:3000")

const oauthStateCookie = "oauth_state"

// GoogleLoginHandler kicks off the OAuth flow: stores a random CSRF state in
// a short-lived cookie and redirects the browser to Google's consent screen.
// Wired as: r.Get("/api/auth/oauth/google", routers.GoogleLoginHandler)
func GoogleLoginHandler(w http.ResponseWriter, r *http.Request) {
	state := oauthusers.RandomState()
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   300,
		SameSite: http.SameSiteLaxMode,
	})
	authURL := googleOAuthConfig.AuthCodeURL(state, oauth2.AccessTypeOnline)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// GoogleCallbackHandler is where Google redirects back to after the user
// approves access. It exchanges the code, fetches the Google profile,
// finds-or-creates the local account (always role "analyst"), issues a
// session token, then sends the browser to the frontend's OAuth landing
// page with that token — which forwards the user to /select-plan.
// Wired as: r.Get("/api/auth/oauth/google/callback", routers.GoogleCallbackHandler)
func GoogleCallbackHandler(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie(oauthStateCookie)
	if err != nil || r.URL.Query().Get("state") != stateCookie.Value {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	token, err := googleOAuthConfig.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusInternalServerError)
		return
	}

	client := googleOAuthConfig.Client(r.Context(), token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		http.Error(w, "failed to fetch google profile", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "failed to read google profile", http.StatusInternalServerError)
		return
	}

	var gUser struct {
		Email         string `json:"email"`
		Name          string `json:"name"`
		VerifiedEmail bool   `json:"verified_email"`
	}
	if err := json.Unmarshal(body, &gUser); err != nil {
		http.Error(w, "failed to parse google profile", http.StatusInternalServerError)
		return
	}
	if gUser.Email == "" {
		http.Error(w, "google account has no email", http.StatusBadRequest)
		return
	}

	user, err := oauthusers.FindOrCreate(gUser.Email, gUser.Name)
	if err != nil {
		http.Error(w, "could not create account", http.StatusInternalServerError)
		return
	}

	sessionToken, err := oauthusers.IssueSession(user)
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}

	// Redirect to frontend with token in URL (no cookie)
	redirectURL := fmt.Sprintf("%s/oauth-callback?token=%s", frontendURL, url.QueryEscape(sessionToken))
	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}

// GoogleSessionHandler resolves a session token (minted by IssueSession)
// back into a user. The frontend's OAuthCallbackPage calls this once, right
// after landing back from Google, to populate its own auth state.
// Wired as: r.Get("/api/auth/oauth/session", routers.GoogleSessionHandler)
func GoogleSessionHandler(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeJSONError(w, http.StatusBadRequest, "missing token")
		return
	}
	user, ok := oauthusers.Lookup(token)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "invalid or expired token")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
