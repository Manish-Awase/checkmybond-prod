// mailer/main.go
// Standalone Go binary invoked by cron every 6 hours.
// Reads all .md and .log files from the export directory,
// filters recipients from email_config.json,
// and sends them as email attachments via SMTP.
//
// Crontab entry:
//   0 */6 * * *  /app/mailer >> /var/log/mailer.log 2>&1
package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"mime/multipart"
	"net/smtp"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ── Config ────────────────────────────────────────────────────────────────────

type SMTPConfig struct {
	Host     string `json:"host"`      // e.g. "smtp.gmail.com"
	Port     string `json:"port"`      // e.g. "587"
	Username string `json:"username"`  // sender address
	Password string `json:"password"`  // app password / SMTP password
	From     string `json:"from"`      // display name + address
}

type Recipient struct {
	Email  string `json:"email"`
	Active bool   `json:"active"` // false = skip without editing crontab
}

type EmailConfig struct {
	SMTP       SMTPConfig  `json:"smtp"`
	Recipients []Recipient `json:"recipients"`
	Subject    string      `json:"subject"` // optional override
}

// ── Entry point ───────────────────────────────────────────────────────────────

func main() {
	logger := log.New(os.Stdout, "[mailer] ", log.LstdFlags)

	exportDir  := envOr("EXPORT_DIR",  "export")
	configPath := envOr("EMAIL_CONFIG", filepath.Join(exportDir, "email_config.json"))

	logger.Printf("=== Mailer run started: %s ===", time.Now().Format(time.RFC1123))

	// 1. Load config
	cfg, err := loadConfig(configPath)
	if err != nil {
		logger.Fatalf("Failed to load email config: %v", err)
	}

	// 2. Collect active recipients
	recipients := activeRecipients(cfg.Recipients)
	if len(recipients) == 0 {
		logger.Println("No active recipients — nothing to send. Exiting.")
		return
	}
	logger.Printf("Active recipients: %v", recipients)

	// 3. Collect .md and .log files from export/
	files, err := collectFiles(exportDir)
	if err != nil {
		logger.Fatalf("Failed to collect files: %v", err)
	}
	if len(files) == 0 {
		logger.Println("No .md or .log files found in export/ — nothing to attach. Exiting.")
		return
	}
	logger.Printf("Files to attach: %v", files)

	// 4. Build subject
	subject := cfg.Subject
	if subject == "" {
		subject = fmt.Sprintf("HITL Platform — Export Report %s", time.Now().Format("2006-01-02 15:04"))
	}

	// 5. Build + send email
	body, err := buildEmail(cfg.SMTP.From, recipients, subject, files)
	if err != nil {
		logger.Fatalf("Failed to build email: %v", err)
	}

	if err := sendEmail(cfg.SMTP, recipients, body); err != nil {
		logger.Fatalf("Failed to send email: %v", err)
	}

	logger.Printf("Email sent successfully to %d recipient(s)", len(recipients))
	logger.Println("=== Mailer run complete ===")
}

// ── Config loader ─────────────────────────────────────────────────────────────

func loadConfig(path string) (*EmailConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg EmailConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

func activeRecipients(all []Recipient) []string {
	var out []string
	for _, r := range all {
		if r.Active && r.Email != "" {
			out = append(out, r.Email)
		}
	}
	return out
}

// ── File collector ────────────────────────────────────────────────────────────

func collectFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext  := strings.ToLower(filepath.Ext(name))
		// Only attach .md and .log files; skip config/checkpoint/backup files
		if (ext == ".md" || ext == ".log") &&
			!strings.HasPrefix(name, "db_backup") &&
			name != "checkpoint.json" {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	return paths, nil
}

// ── Email builder ─────────────────────────────────────────────────────────────

func buildEmail(from string, to []string, subject string, attachments []string) ([]byte, error) {
	var buf strings.Builder

	// Headers
	buf.WriteString(fmt.Sprintf("From: %s\r\n", from))
	buf.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(to, ", ")))
	buf.WriteString(fmt.Sprintf("Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject)))
	buf.WriteString("MIME-Version: 1.0\r\n")

	// Multipart body
	mw := multipart.NewWriter(&buf) // writes boundary header
	buf.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", mw.Boundary()))

	// ── Plain text body part ──
	th := make(textproto.MIMEHeader)
	th.Set("Content-Type", "text/plain; charset=utf-8")
	tw, err := mw.CreatePart(th)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(tw,
		"HITL Platform — Scheduled Export\n\nGenerated: %s\nFiles attached: %d\n\nSee attachments for extracted data and logs.\n",
		time.Now().Format(time.RFC1123), len(attachments),
	)

	// ── One part per attachment ──
	for _, fpath := range attachments {
		data, err := os.ReadFile(fpath)
		if err != nil {
			return nil, fmt.Errorf("read attachment %s: %w", fpath, err)
		}

		fname := filepath.Base(fpath)
		ct    := "text/plain; charset=utf-8"
		if strings.HasSuffix(fname, ".md") {
			ct = "text/markdown; charset=utf-8"
		}

		ah := make(textproto.MIMEHeader)
		ah.Set("Content-Type", ct)
		ah.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fname))
		ah.Set("Content-Transfer-Encoding", "base64")

		aw, err := mw.CreatePart(ah)
		if err != nil {
			return nil, err
		}

		// Base64 encode in 76-char lines (RFC 2045)
		encoded := encodeBase64Lines(data)
		aw.Write([]byte(encoded))
	}

	mw.Close()
	return []byte(buf.String()), nil
}

// ── SMTP sender ───────────────────────────────────────────────────────────────

func sendEmail(cfg SMTPConfig, to []string, body []byte) error {
	addr := cfg.Host + ":" + cfg.Port
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)

	// TLS config
	tlsCfg := &tls.Config{ServerName: cfg.Host}

	// Connect
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		// Fallback to STARTTLS on port 587
		client, err2 := smtp.Dial(addr)
		if err2 != nil {
			return fmt.Errorf("smtp dial: %w (tls err: %v)", err2, err)
		}
		if err2 = client.StartTLS(tlsCfg); err2 != nil {
			return fmt.Errorf("starttls: %w", err2)
		}
		return sendViaClient(client, auth, cfg.From, to, body)
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp new client: %w", err)
	}
	return sendViaClient(client, auth, cfg.From, to, body)
}

func sendViaClient(client *smtp.Client, auth smtp.Auth, from string, to []string, body []byte) error {
	defer client.Quit()

	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, addr := range to {
		if err := client.Rcpt(addr); err != nil {
			return fmt.Errorf("smtp RCPT TO %s: %w", addr, err)
		}
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	defer wc.Close()
	_, err = wc.Write(body)
	return err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// encodeBase64Lines encodes bytes to base64 with 76-char line wrapping (RFC 2045).
func encodeBase64Lines(data []byte) string {
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var sb strings.Builder
	col := 0

	for i := 0; i < len(data); i += 3 {
		var b [3]byte
		n := copy(b[:], data[i:])
		var enc [4]byte
		enc[0] = alpha[b[0]>>2]
		enc[1] = alpha[((b[0]&0x3)<<4)|(b[1]>>4)]
		if n > 1 {
			enc[2] = alpha[((b[1]&0xf)<<2)|(b[2]>>6)]
		} else {
			enc[2] = '='
		}
		if n > 2 {
			enc[3] = alpha[b[2]&0x3f]
		} else {
			enc[3] = '='
		}
		sb.Write(enc[:])
		col += 4
		if col >= 76 {
			sb.WriteString("\r\n")
			col = 0
		}
	}
	if col > 0 {
		sb.WriteString("\r\n")
	}
	return sb.String()
}
