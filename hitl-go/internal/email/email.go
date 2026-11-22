// internal/email/email.go
//
// EMAIL_PROVIDER selects how every Send* function below actually
// delivers mail -- one switch, not per-function logic, so adding a
// provider or changing which one is active never touches call sites in
// routers/auth.go or routers/contact.go.
//
//	EMAIL_PROVIDER=sendgrid (default) -- requires SENDGRID_API_KEY and
//	  SENDGRID_FROM_EMAIL. The sender identity (SENDGRID_FROM_EMAIL) must
//	  be a verified sender or verified domain in your SendGrid account,
//	  or SendGrid will reject the send outright -- this is the single
//	  most common reason a "correctly configured" SendGrid integration
//	  silently fails in testing.
//
//	EMAIL_PROVIDER=brevo -- requires BREVO_API_KEY and BREVO_FROM_EMAIL.
//	  Talks to Brevo's transactional email API (api.brevo.com/v3/smtp/email)
//	  directly over net/http -- no SDK, so switching TO brevo never means
//	  waiting on a new Go dependency to resolve, same reasoning as every
//	  other provider in this file. Like SendGrid, BREVO_FROM_EMAIL must be
//	  a verified sender in your Brevo account or the send is rejected.
//
//	EMAIL_PROVIDER=smtp -- requires SMTP_HOST, SMTP_PORT, SMTP_FROM_EMAIL,
//	  and (unless your relay allows unauthenticated/IP-allowlisted
//	  sending) SMTP_USERNAME + SMTP_PASSWORD. Handles both STARTTLS
//	  (port 587, and 25 -- the common case for nearly every provider:
//	  Gmail's SMTP relay, Amazon SES, Mailgun, Postmark, Office365, a
//	  standard Postfix relay) and implicit TLS (port 465, required by
//	  some providers/older configurations) -- net/smtp's stdlib
//	  SendMail only handles the former, so port 465 is special-cased
//	  below with a manual TLS dial.
//
//	Anything else, or EMAIL_PROVIDER unset -- falls back to logging the
//	email to stdout, same as before. Local dev never needs real
//	credentials for either provider.
//
// Switching providers is a one-line change: set EMAIL_PROVIDER (and that
// provider's credentials) and redeploy. No call site in routers/auth.go,
// routers/contact.go, or the alert engine changes, because all of them
// go through the unexported send() choke point below rather than calling
// a specific provider directly.
//
// All three exported functions are fire-and-forget: they return no
// error (matching their existing call sites -- some already called via
// `go iemail.SendX(...)`), and log to stdout on failure rather than
// propagating an error the caller would have to handle.
package email

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"os"
	"time"

	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
)

func provider() string {
	return os.Getenv("EMAIL_PROVIDER")
}

func fromName() string {
	if v := os.Getenv("EMAIL_FROM_NAME"); v != "" {
		return v
	}
	return "CheckMyBond"
}

// send is the single choke point every exported function below goes
// through. It uses EMAIL_PROVIDER.
func send(toEmail, subject, plainBody string) error {
	return sendVia(provider(), toEmail, subject, plainBody)
}

// sendVia delivers through an explicitly named provider. send() passes
// EMAIL_PROVIDER; SendAlert (alert.go) passes ALERT_EMAIL_PROVIDER when
// set, so alerts can use a different provider than password reset/OTP.
func sendVia(prov, toEmail, subject, plainBody string) error {
	switch prov {
	case "sendgrid", "":
		// sendgrid is also the default for an EMPTY EMAIL_PROVIDER --
		// matches "you already have SendGrid set up" as the expected
		// common case. Set EMAIL_PROVIDER=stdout explicitly for local
		// dev without credentials, rather than relying on omission.
		if os.Getenv("SENDGRID_API_KEY") == "" {
			fmt.Printf("[EMAIL] To: %s | Subject: %s\n%s\n", toEmail, subject, plainBody)
			return nil
		}
		return sendViaSendGrid(toEmail, subject, plainBody)
	case "brevo":
		// Same reasoning as the sendgrid branch above: missing
		// credentials fall back to printing rather than erroring, so
		// local dev never needs real Brevo credentials either.
		if os.Getenv("BREVO_API_KEY") == "" || os.Getenv("BREVO_FROM_EMAIL") == "" {
			fmt.Printf("[EMAIL] To: %s | Subject: %s\n%s\n", toEmail, subject, plainBody)
			return nil
		}
		return sendViaBrevo(toEmail, subject, plainBody)
	case "smtp":
		return sendViaSMTP(toEmail, subject, plainBody)
	case "stdout":
		fmt.Printf("[EMAIL] To: %s | Subject: %s\n%s\n", toEmail, subject, plainBody)
		return nil
	default:
		return fmt.Errorf("unknown EMAIL_PROVIDER %q (want sendgrid, brevo, smtp, or stdout)", prov)
	}
}

// ── SendGrid ─────────────────────────────────────────────────────────────────

func sendViaSendGrid(toEmail, subject, plainBody string) error {
	apiKey := os.Getenv("SENDGRID_API_KEY")
	fromEmail := os.Getenv("SENDGRID_FROM_EMAIL")
	if fromEmail == "" {
		return fmt.Errorf("EMAIL_PROVIDER=sendgrid but SENDGRID_FROM_EMAIL is not set")
	}

	from := mail.NewEmail(fromName(), fromEmail)
	to := mail.NewEmail("", toEmail)
	// No HTML body (last arg "") -- plain text only, kept identical in
	// content to the SMTP path so behavior doesn't diverge by provider.
	message := mail.NewSingleEmail(from, subject, to, plainBody, "")

	client := sendgrid.NewSendClient(apiKey)
	resp, err := client.Send(message)
	if err != nil {
		return fmt.Errorf("sendgrid send failed: %w", err)
	}
	if resp.StatusCode >= 300 {
		// SendGrid returns 202 on success. A 4xx here is almost always
		// either an unverified sender identity (SENDGRID_FROM_EMAIL not
		// verified in your SendGrid account) or a bad/revoked API key --
		// the response body usually says which.
		return fmt.Errorf("sendgrid returned status %d: %s", resp.StatusCode, resp.Body)
	}
	return nil
}

// ── Brevo ────────────────────────────────────────────────────────────────────
//
// Brevo's Go SDK pulls in its own dependency tree for one JSON POST, so
// this talks to the transactional email API directly -- same reasoning
// as the rest of this codebase's zero-new-dependency stance (see
// internal/metrics's hand-rolled Prometheus registry). Docs:
// https://developers.brevo.com/reference/sendtransacemail

const brevoSendURL = "https://api.brevo.com/v3/smtp/email"

type brevoContact struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type brevoSendRequest struct {
	Sender      brevoContact   `json:"sender"`
	To          []brevoContact `json:"to"`
	Subject     string         `json:"subject"`
	TextContent string         `json:"textContent"`
}

func sendViaBrevo(toEmail, subject, plainBody string) error {
	apiKey := os.Getenv("BREVO_API_KEY")
	fromEmail := os.Getenv("BREVO_FROM_EMAIL")
	// Credentials are already checked by the caller (send()) before this
	// is reached in the normal flow, but this function can be called
	// directly too, so it still validates rather than trusting the
	// caller silently.
	if apiKey == "" || fromEmail == "" {
		return fmt.Errorf("EMAIL_PROVIDER=brevo but BREVO_API_KEY/BREVO_FROM_EMAIL are not fully set")
	}

	payload := brevoSendRequest{
		Sender:      brevoContact{Email: fromEmail, Name: fromName()},
		To:          []brevoContact{{Email: toEmail}},
		Subject:     subject,
		TextContent: plainBody,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("brevo request encode failed: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, brevoSendURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("brevo request build failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Brevo authenticates via this header, not Authorization: Bearer --
	// easy to get wrong once and never notice, since a bad key here
	// comes back as a 401 that looks identical to a missing header.
	req.Header.Set("api-key", apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("brevo send failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		// Brevo returns 201 on success and a JSON {"message": "..."}
		// body on failure -- the most common cases are an unverified
		// BREVO_FROM_EMAIL sender/domain, or a key scoped without
		// transactional-email send permission.
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("brevo returned status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// ── SMTP ─────────────────────────────────────────────────────────────────────

func sendViaSMTP(toEmail, subject, plainBody string) error {
	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	username := os.Getenv("SMTP_USERNAME")
	password := os.Getenv("SMTP_PASSWORD")
	fromEmail := os.Getenv("SMTP_FROM_EMAIL")

	if host == "" || port == "" || fromEmail == "" {
		return fmt.Errorf("EMAIL_PROVIDER=smtp but SMTP_HOST/SMTP_PORT/SMTP_FROM_EMAIL are not fully set")
	}

	addr := host + ":" + port
	msg := buildPlainMessage(fromEmail, fromName(), toEmail, subject, plainBody)

	var auth smtp.Auth
	if username != "" || password != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}

	if port == "465" {
		return sendImplicitTLS(addr, host, auth, fromEmail, toEmail, msg)
	}
	// Handles STARTTLS (587, and plain 25) on its own -- dials plain TCP,
	// then upgrades if the server advertises the STARTTLS extension,
	// which every provider listed in the package comment does on 587.
	if err := smtp.SendMail(addr, auth, fromEmail, []string{toEmail}, msg); err != nil {
		return fmt.Errorf("smtp send failed: %w", err)
	}
	return nil
}

// sendImplicitTLS handles port 465, where the connection must be TLS
// from the very first byte -- net/smtp.SendMail can't do this on its
// own, since it always dials plain TCP first and only upgrades via
// STARTTLS if offered mid-conversation.
func sendImplicitTLS(addr, host string, auth smtp.Auth, fromEmail, toEmail string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return fmt.Errorf("smtp tls dial failed: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp client init failed: %w", err)
	}
	defer client.Close()

	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth failed: %w", err)
		}
	}
	if err := client.Mail(fromEmail); err != nil {
		return fmt.Errorf("smtp MAIL FROM failed: %w", err)
	}
	if err := client.Rcpt(toEmail); err != nil {
		return fmt.Errorf("smtp RCPT TO failed: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA failed: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func buildPlainMessage(fromEmail, fromDisplayName, toEmail, subject, body string) []byte {
	headers := fmt.Sprintf(
		"From: %s <%s>\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\n\r\n",
		fromDisplayName, fromEmail, toEmail, subject,
	)
	return []byte(headers + body)
}

// ── Exported — these are what routers/auth.go and routers/contact.go call ──

func SendPasswordReset(toEmail, resetURL string) {
	subject := "Reset your CheckMyBond password"
	body := fmt.Sprintf(
		"You requested a password reset for your CheckMyBond account.\n\n"+
			"Reset your password: %s\n\n"+
			"This link expires in 15 minutes. If you didn't request this, you can safely ignore this email.",
		resetURL,
	)
	if err := send(toEmail, subject, body); err != nil {
		fmt.Printf("[email] SendPasswordReset failed for %s: %v\n", toEmail, err)
	}
}

func SendLoginOTP(toEmail, code string) {
	subject := "Your CheckMyBond verification code"
	body := fmt.Sprintf(
		"Your one-time login verification code is: %s\n\n"+
			"This code expires in 5 minutes. If you didn't try to log in, you can safely ignore this email.",
		code,
	)
	if err := send(toEmail, subject, body); err != nil {
		fmt.Printf("[email] SendLoginOTP failed for %s: %v\n", toEmail, err)
	}
}

func SendContactSubmission(destEmail, visitorName, visitorEmail, message string) {
	subject := fmt.Sprintf("New contact form message from %s", visitorName)
	body := fmt.Sprintf("From: %s <%s>\n\n%s", visitorName, visitorEmail, message)
	if err := send(destEmail, subject, body); err != nil {
		fmt.Printf("[email] SendContactSubmission failed: %v\n", err)
	}
}
