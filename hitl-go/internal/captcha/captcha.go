// internal/captcha/captcha.go
//
// Thin client for hCaptcha's siteverify endpoint. Swap the constant URL
// and field names if you'd rather use reCAPTCHA -- the request shape
// (secret + response token + optional remoteip, as a form-encoded POST)
// is nearly identical between the two providers.
package captcha

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const verifyURL = "https://hcaptcha.com/siteverify"

var httpClient = &http.Client{Timeout: 5 * time.Second}

type verifyResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
}

// Verify checks a CAPTCHA response token (the value the frontend widget
// hands back after the user solves the challenge) against hCaptcha's
// servers. remoteIP is optional but improves hCaptcha's own risk scoring
// -- pass the real client IP when you have it.
//
// Returns (false, nil) for a token that hCaptcha explicitly rejected
// (wrong/expired/already-used token) -- that's a normal "captcha failed"
// case, not an error. Returns (false, err) only when verification itself
// couldn't be completed (network failure, misconfiguration, etc.) --
// callers should treat that as "captcha required, try again" too, since
// failing open here would defeat the whole point.
func Verify(ctx context.Context, token, remoteIP string) (bool, error) {
	secret := os.Getenv("HCAPTCHA_SECRET")
	if secret == "" {
		return false, fmt.Errorf("HCAPTCHA_SECRET not set")
	}
	if token == "" {
		return false, nil
	}

	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, verifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return false, fmt.Errorf("build captcha verify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("captcha verify request failed: %w", err)
	}
	defer resp.Body.Close()

	var result verifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("decode captcha verify response: %w", err)
	}

	return result.Success, nil
}
