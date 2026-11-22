// routers/auth.go
package routers

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	iauth "hitl-platform/internal/auth"
	icaptcha "hitl-platform/internal/captcha"
	idb "hitl-platform/internal/db"
	iemail "hitl-platform/internal/email"
	ilogin "hitl-platform/internal/loginattempts"
	imw "hitl-platform/internal/middleware"
	iotp "hitl-platform/internal/otp"
	iqueue "hitl-platform/internal/queue"
)

// captchaFailThreshold is how many failed attempts (within the 15-minute
// window tracked by internal/loginattempts) an identifier can rack up
// before a valid CAPTCHA token becomes required on top of the password.
// Overridable via CAPTCHA_FAIL_THRESHOLD.
func captchaFailThreshold() int64 {
	if v := os.Getenv("CAPTCHA_FAIL_THRESHOLD"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 5
}

// otpFailThreshold is the (higher) failure count at which a CORRECT
// password is no longer enough on its own -- the account also has to
// prove control of its email inbox via a one-time code. This exists on
// top of the CAPTCHA gate, not instead of it: CAPTCHA stops scripted
// abuse, this stops a human attacker who has a valid stolen password
// (credential stuffing) and is willing to solve CAPTCHAs. Overridable via
// OTP_FAIL_THRESHOLD; should stay comfortably above captchaFailThreshold.
func otpFailThreshold() int64 {
	if v := os.Getenv("OTP_FAIL_THRESHOLD"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 10
}

// clientIP does a best-effort extraction of the real client IP, preferring
// the headers a reverse proxy/gateway sets over raw RemoteAddr. Used only
// to improve hCaptcha's own risk scoring -- never for security decisions
// on its own.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// normalizeEmail canonicalizes Gmail/Googlemail addresses before they're
// checked for uniqueness or stored: Gmail ignores dots in the local part
// and treats anything after a "+" as a tag, so "j.doe@gmail.com",
// "jdoe@gmail.com", and "jdoe+freebie@gmail.com" all deliver to the same
// real inbox even though they're three different strings. Without this,
// one person can register unlimited accounts from a single Gmail address
// and re-claim the free plan every time (see ClaimFreeHandler) with zero
// friction. Scoped to gmail.com/googlemail.com specifically -- other
// providers don't reliably support this behavior, and normalizing their
// addresses the same way would incorrectly merge genuinely distinct
// mailboxes. This is NOT a substitute for real email verification: it
// closes the single most common/trivial abuse pattern cheaply, but
// someone using unrelated throwaway addresses (temp-mail services, etc.)
// still isn't caught by this alone.
func normalizeEmail(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return email
	}
	local, domain := email[:at], email[at+1:]
	if domain != "gmail.com" && domain != "googlemail.com" {
		return email
	}
	if plus := strings.Index(local, "+"); plus >= 0 {
		local = local[:plus]
	}
	local = strings.ReplaceAll(local, ".", "")
	return local + "@gmail.com" // googlemail.com is Gmail's own alias for gmail.com
}

func AuthRouter() http.Handler {
	r := chi.NewRouter()

	// Public — no Identity middleware
	r.Post("/register", handleRegister)
	r.Post("/login", handleLogin)
	r.Post("/forgot-password", handleForgotPassword)
	r.Post("/reset-password", handleResetPassword)

	// Protected — Identity middleware enforces gateway headers
	r.Group(func(r chi.Router) {
		r.Use(imw.Identity)
		r.Get("/me", handleMe)
		r.Post("/logout", handleLogout)

		// Admin only
		r.Group(func(r chi.Router) {
			r.Use(imw.RequireRole("admin"))
			r.Get("/users", handleListUsers)
			r.Post("/users", handleAdminCreateUser)
			r.Patch("/users/{user_id}/deactivate", handleDeactivateUser)
		})
	})

	return r
}

// ── Register ──────────────────────────────────────────────────────────────────

// accountRoles is the allow-list of roles an account can hold.
var accountRoles = map[string]bool{"analyst": true, "reviewer": true, "admin": true, "debug": true}

// publicSignupRole is the ONLY role the public /register endpoint ever
// grants.
const publicSignupRole = "analyst"

type registerRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// createAccount validates a signup and creates the account. On any problem
// it writes the error response itself and returns ok=false.
//
// `role` is passed in by the CALLER and deliberately not read from req:
// who is allowed to choose a role is a decision for the endpoint (public
// signup never; an admin, from an allow-list), not for this shared code.
func createAccount(w http.ResponseWriter, r *http.Request, req registerRequest, role string) (user *idb.User, ok bool) {
	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	req.Email = normalizeEmail(req.Email)

	if len(req.Username) < 3 {
		errJSON(w, http.StatusUnprocessableEntity, "name must be at least 3 characters")
		return nil, false
	}
	// The name is display-only and may be shared by any number of
	// accounts -- the ONLY thing that must be unique is the email, so
	// there is intentionally no "name already taken" check here. One
	// reserved name aside, see isReservedUsername for why.
	if isReservedUsername(req.Username) {
		errJSON(w, http.StatusUnprocessableEntity, "that name is reserved, please choose another")
		return nil, false
	}
	if !validEmail(req.Email) {
		errJSON(w, http.StatusUnprocessableEntity, "enter a valid email address")
		return nil, false
	}
	if len(req.Password) < 8 {
		errJSON(w, http.StatusUnprocessableEntity, "password must be at least 8 characters")
		return nil, false
	}

	ctx := r.Context()

	// Fast, friendly path. NOT the guarantee: two simultaneous signups
	// can both pass this check, which is why CreateUser below is what
	// actually enforces it (the database's unique index on lower(email)),
	// and why its ErrEmailTaken is handled as the same 409.
	if existing, _ := idb.GetUserByEmail(ctx, req.Email); existing != nil {
		errJSON(w, http.StatusConflict, "email already registered")
		return nil, false
	}

	hash, err := iauth.HashPassword(req.Password)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "password hashing failed")
		return nil, false
	}

	user, err = idb.CreateUser(ctx, uuid.New().String(), req.Username, req.Email, hash, role)
	if err != nil {
		if errors.Is(err, idb.ErrEmailTaken) {
			errJSON(w, http.StatusConflict, "email already registered")
			return nil, false
		}
		// Real detail to the server log, generic message to the client.
		log.Printf("register: could not create user (email %q): %v", req.Email, err)
		errJSON(w, http.StatusInternalServerError, "could not create user")
		return nil, false
	}
	return user, true
}

// handleRegister is PUBLIC signup: anyone on the internet can call it (the
// gateway lets /api/auth/register through without a login).
//
// SECURITY: any "role" in the request body is IGNORED -- every account made
// here is an analyst. This used to trust the body, which let a stranger
// register as "admin" (or "debug") with one request: the role picker on the
// admin page was only a frontend route guard, and anyone can call the API
// directly. Accounts with other roles are made by an admin through
// handleAdminCreateUser, or promoted in the database.
func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	user, ok := createAccount(w, r, req, publicSignupRole)
	if !ok {
		return
	}

	token, err := iauth.CreateAccessToken(user.ID, user.Username, user.Role)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not create token")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"access_token": token,
		"token_type":   "bearer",
		"user_id":      user.ID,
		"username":     user.Username,
		"email":        user.Email,
		"role":         user.Role,
	})
}

// handleAdminCreateUser lets an ADMIN create an account with a chosen role.
// Admin-only: it sits in the RequireRole("admin") group, and the gateway
// verifies the caller's token for it (it is not a public path).
//
// Unlike public signup it returns NO access token. The caller is an admin
// creating someone else's account; handing back a session for the new user
// would let the admin's browser swap into it (the old admin page did exactly
// that by reusing the public signup call).
func handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	role := strings.ToLower(strings.TrimSpace(req.Role))
	if role == "" {
		role = publicSignupRole
	}
	if !accountRoles[role] {
		errJSON(w, http.StatusUnprocessableEntity, "role must be one of: analyst, reviewer, admin, debug")
		return
	}
	user, ok := createAccount(w, r, req, role)
	if !ok {
		return
	}
	// A record of who granted what -- a privileged account being created is
	// exactly the event you'd want to be able to look back at.
	log.Printf("admin %s created account %s (%s) with role %q", imw.UserID(r), user.ID, user.Email, user.Role)

	writeJSON(w, http.StatusCreated, map[string]any{
		"user_id":  user.ID,
		"username": user.Username,
		"email":    user.Email,
		"role":     user.Role,
	})
}

// ── Login ─────────────────────────────────────────────────────────────────────
//
// Layered defenses, in order:
//   1. CAPTCHA gate (captchaFailThreshold) — must pass before the password
//      is even checked, applies regardless of whether the account exists.
//   2. Password check — unchanged from before.
//   3. OTP step-up gate (otpFailThreshold) — only reached once the
//      password is ALREADY correct. If the account has crossed the OTP
//      threshold, a correct password alone no longer issues a token: an
//      emailed code must also be verified. This is what stops credential
//      stuffing specifically — a leaked-but-correct password stops being
//      sufficient once an account is under sustained attack.
//
// Two-phase flow for step 3: first request (password correct, no
// otp_code) sends the code and returns {"otp_required": true} WITHOUT a
// token. Client resubmits login+password+otp_code; only then is a token
// issued.

func handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		// Holds the account's EMAIL. The key is still called "login" so
		// existing clients keep working; a username is not accepted
		// because usernames are not unique (see auth_identity.go).
		Login        string `json:"login"`
		Password     string `json:"password"`
		CaptchaToken string `json:"captcha_token"`
		OTPCode      string `json:"otp_code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Failed-login counters are tracked by this exact string, so it's
	// the NORMALIZED email: otherwise someone could reset their own
	// counter by alternating Gmail dot-variants of their address between
	// attempts.
	candidates := loginEmailCandidates(req.Login)
	loginVal := normalizeEmail(strings.TrimSpace(req.Login))
	ctx := r.Context()

	failCount, err := ilogin.Count(ctx, loginVal)
	if err != nil {
		// Redis being unreachable shouldn't lock everyone out of login --
		// proceed as if no failures are on record. Fails OPEN on
		// availability; the checks below still enforce correctly once a
		// threshold IS known to have been crossed.
		failCount = 0
	}

	if failCount >= captchaFailThreshold() {
		ok, verr := icaptcha.Verify(ctx, req.CaptchaToken, clientIP(r))
		if verr != nil || !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"detail":           "captcha verification required",
				"captcha_required": true,
			})
			return
		}
	}

	// Identify the account by email -- never by name. Each candidate form
	// is tried and the first whose account also has this password wins,
	// so a legacy account stored under the as-typed spelling still gets
	// in even if a different account owns the normalized one.
	var user *idb.User
	for _, cand := range candidates {
		u, _ := idb.GetUserByEmail(ctx, cand)
		if u != nil && iauth.VerifyPassword(req.Password, u.PasswordHash) {
			user = u
			break
		}
	}

	if user == nil {
		newCount, cerr := ilogin.RecordFailure(ctx, loginVal)
		if cerr != nil {
			newCount = failCount + 1 // best-effort estimate if Redis write failed
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"detail":           "invalid email or password",
			"captcha_required": newCount >= captchaFailThreshold(),
		})
		return
	}
	if !user.IsActive {
		errJSON(w, http.StatusForbidden, "account is disabled. Contact an administrator.")
		return
	}

	// ── OTP step-up ──────────────────────────────────────────────────────
	// Password is correct at this point. failCount reflects PRIOR failures
	// (before this successful attempt) -- if that's already past the OTP
	// threshold, this success alone isn't trusted yet.
	if failCount >= otpFailThreshold() {
		if req.OTPCode == "" {
			code, cerr := iotp.GenerateCode()
			if cerr != nil {
				errJSON(w, http.StatusInternalServerError, "could not generate verification code")
				return
			}
			switch serr := iotp.Store(ctx, loginVal, code); serr {
			case nil:
				go iemail.SendLoginOTP(user.Email, code)
			case iotp.ErrAlreadyOutstanding:
				// A code from an earlier attempt is still valid -- don't
				// send a second email, the user already has one.
			default:
				errJSON(w, http.StatusInternalServerError, "could not start verification")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"otp_required": true,
				"message":      "we sent a verification code to your email",
			})
			return
		}

		ok, operr := iotp.Verify(ctx, loginVal, req.OTPCode)
		if operr != nil || !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"detail":       "invalid or expired verification code",
				"otp_required": true,
			})
			return
		}
		// OTP verified — fall through to issue the token below.
	}

	_ = ilogin.Reset(ctx, loginVal)
	_ = idb.UpdateLastLogin(ctx, user.ID)

	token, err := iauth.CreateAccessToken(user.ID, user.Username, user.Role)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not create token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "bearer",
		"user_id":      user.ID,
		"username":     user.Username,
		"email":        user.Email,
		"role":         user.Role,
	})
}

// ── Me ────────────────────────────────────────────────────────────────────────

func handleMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, err := idb.GetUserByID(ctx, imw.UserID(r))
	if err != nil || user == nil {
		errJSON(w, http.StatusNotFound, "user not found")
		return
	}
	var lastLogin *string
	if user.LastLogin != nil {
		s := user.LastLogin.Format(time.RFC3339)
		lastLogin = &s
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":    user.ID,
		"username":   user.Username,
		"email":      user.Email,
		"role":       user.Role,
		"is_active":  user.IsActive,
		"created_at": user.CreatedAt.Format(time.RFC3339),
		"last_login": lastLogin,
	})
}

// ── Logout ────────────────────────────────────────────────────────────────────

func handleLogout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := imw.UserID(r)
	tokenHash := imw.TokenHash(r)
	tokenExpS := imw.TokenExp(r)

	if tokenHash != "" {
		var ttl int64 = 86400 // default 24h
		if tokenExpS != "" {
			if exp, err := strconv.ParseInt(tokenExpS, 10, 64); err == nil {
				ttl = exp - time.Now().Unix()
				if ttl < 1 {
					ttl = 1
				}
			}
		}
		_ = iqueue.BlacklistToken(ctx, tokenHash, userID, ttl)
	}

	_ = idb.UpdateLastLogout(ctx, userID)

	writeJSON(w, http.StatusOK, map[string]any{
		"logged_out": true,
		"user_id":    userID,
	})
}

// ── List users ────────────────────────────────────────────────────────────────

func handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := idb.ListUsers(r.Context())
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not fetch users")
		return
	}
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		var lastLogin *string
		if u.LastLogin != nil {
			s := u.LastLogin.Format(time.RFC3339)
			lastLogin = &s
		}
		out = append(out, map[string]any{
			"user_id":    u.ID,
			"username":   u.Username,
			"email":      u.Email,
			"role":       u.Role,
			"is_active":  u.IsActive,
			"created_at": u.CreatedAt.Format(time.RFC3339),
			"last_login": lastLogin,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Deactivate user ───────────────────────────────────────────────────────────

func handleDeactivateUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "user_id")
	requester := imw.UserID(r)

	if userID == requester {
		errJSON(w, http.StatusBadRequest, "cannot deactivate your own account")
		return
	}

	ctx := r.Context()
	user, err := idb.GetUserByID(ctx, userID)
	if err != nil || user == nil {
		errJSON(w, http.StatusNotFound, "user not found")
		return
	}

	if err := idb.SetUserActive(ctx, userID, false); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not deactivate user")
		return
	}

	_ = iqueue.BlacklistUserFlag(ctx, userID)

	writeJSON(w, http.StatusOK, map[string]any{
		"deactivated": true,
		"user_id":     userID,
	})
}

// ── Forgot password ───────────────────────────────────────────────────────────

func handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid body")
		return
	}

	email := normalizeEmail(req.Email)
	ctx := r.Context()

	user, err := idb.GetUserByEmail(ctx, email)
	if err != nil || user == nil {
		writeJSON(w, http.StatusOK, map[string]any{"message": "if account exists, email sent"})
		return
	}

	rawToken, tokenHash, err := iauth.GenerateResetToken()
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not generate reset token")
		return
	}

	expiresAt := time.Now().UTC().Add(15 * time.Minute)
	if err := idb.SavePasswordReset(ctx, tokenHash, user.Email, expiresAt); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not save reset token")
		return
	}

	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "http://localhost:3000"
	}
	resetURL := frontendURL + "/reset-password?token=" + rawToken

	go iemail.SendPasswordReset(user.Email, resetURL)

	writeJSON(w, http.StatusOK, map[string]any{"message": "if account exists, email sent"})
}

// ── Reset password ────────────────────────────────────────────────────────────

func handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid body")
		return
	}

	if len(req.NewPassword) < 8 {
		errJSON(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	ctx := r.Context()

	tokenHash := iauth.HashResetToken(req.Token)
	email, err := idb.ValidatePasswordReset(ctx, tokenHash)
	if err != nil {
		errJSON(w, http.StatusBadRequest, "invalid or expired reset token")
		return
	}

	user, err := idb.GetUserByEmail(ctx, email)
	if err != nil || user == nil {
		errJSON(w, http.StatusBadRequest, "invalid or expired reset token")
		return
	}

	newHash, err := iauth.HashPassword(req.NewPassword)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "password hashing failed")
		return
	}

	if err := idb.UpdatePassword(ctx, user.ID, newHash); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not update password")
		return
	}

	_ = idb.DeletePasswordReset(ctx, tokenHash)

	writeJSON(w, http.StatusOK, map[string]any{"message": "password updated"})
}
