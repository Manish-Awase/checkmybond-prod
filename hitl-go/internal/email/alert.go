// internal/email/alert.go
//
// Alert delivery, added as a separate file so email.go stays untouched.
//
// SendAlert goes through the same unexported send() choke point as every
// other mail in this package, so it inherits EMAIL_PROVIDER and needs no
// configuration of its own. If password resets are already arriving,
// alerts will arrive too — and if they are not, that is worth knowing
// before an incident, which is what the "send test alert" button in the
// Alerts screen is for.
//
// # DELIBERATELY DIFFERENT FROM THE REST OF THIS PACKAGE
//
// The other Send* functions here are fire-and-forget: they swallow errors
// and log to stdout. SendAlert returns its error instead, because the
// alert engine records delivery failures on the alert_events row and
// surfaces them in the UI. An alert system that cannot tell you it failed
// to alert you is not much of an alert system.
package email

import "os"

// SendAlert delivers one alert email. Returns the delivery error, if any.
func SendAlert(toEmail, subject, plainBody string) error {
	return sendVia(alertProvider(), toEmail, subject, plainBody)
}

// alertProvider returns ALERT_EMAIL_PROVIDER when set (e.g. "brevo" for
// alerts while EMAIL_PROVIDER=sendgrid handles password reset / OTP /
// contact mail). Unset -> falls back to EMAIL_PROVIDER, so existing
// deployments behave exactly as before.
func alertProvider() string {
	if v := os.Getenv("ALERT_EMAIL_PROVIDER"); v != "" {
		return v
	}
	return os.Getenv("EMAIL_PROVIDER")
}

// AlertProviderName reports which provider alerts will go through, for
// display in the Alerts screen. Mirrors the switch in send().
func AlertProviderName() string {
	switch alertProvider() {
	case "sendgrid", "":
		if os.Getenv("SENDGRID_API_KEY") == "" {
			// send() silently falls back to printing when the key is
			// missing. Reporting "sendgrid" here would be a lie that
			// takes an incident to discover.
			return "stdout (SENDGRID_API_KEY is not set — alerts are only printed to the server log)"
		}
		return "sendgrid"
	case "brevo":
		if os.Getenv("BREVO_API_KEY") == "" || os.Getenv("BREVO_FROM_EMAIL") == "" {
			return "stdout (BREVO_API_KEY/BREVO_FROM_EMAIL not fully set — alerts are only printed to the server log)"
		}
		return "brevo"
	case "smtp":
		return "smtp"
	case "stdout":
		return "stdout"
	default:
		return alertProvider()
	}
}
