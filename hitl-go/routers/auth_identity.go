// routers/auth_identity.go
//
// How a person is identified at registration and login.
//
//	EMAIL is the one unique identity on an account. It's what you sign
//	in with, what password reset keys on, and what the database enforces
//	as unique (migrations/0006_username_not_unique.sql).
//
//	The USERNAME is only a display name. Any number of accounts may
//	share one, so nothing may look an account up by it -- there is
//	deliberately no idb.GetUserByUsername.
//
// The rules live here, apart from the HTTP handlers, so they can be
// tested directly (auth_identity_test.go).
package routers

import (
	"net/mail"
	"strings"
)

// maxEmailLen is RFC 5321's cap on an address; users.email is VARCHAR(255).
const maxEmailLen = 254

// validEmail reports whether s is a single, plain email address.
//
// Needed now that email is the sole identity: registering with "" or with
// something that isn't an address would create an account nobody could
// meaningfully sign in to (and would permanently occupy that unique
// value). s should already be trimmed/lowercased (normalizeEmail).
//
// Strict on purpose: net/mail also accepts "Jane <j@x.com>" and similar;
// requiring the parsed address to equal the input rejects those, so only
// a bare address is stored. The domain must contain a dot -- this is a
// sanity check, not a deliverability check (that needs a verification
// email, which is a separate feature).
func validEmail(s string) bool {
	if s == "" || len(s) > maxEmailLen {
		return false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return false
	}
	at := strings.LastIndex(s, "@")
	return at > 0 && strings.Contains(s[at+1:], ".")
}

// loginEmailCandidates returns the email forms to try for a login, in
// order: the normalized form first (what new accounts are stored as),
// then the address exactly as typed (trimmed, lowercased) if that
// differs.
//
// The second form exists for accounts whose email was stored before
// normalizeEmail existed -- e.g. "j.doe@gmail.com", which normalizes to
// "jdoe@gmail.com" and would otherwise no longer match its own row. They
// used to be able to sign in by username; with username login gone, this
// keeps them from being locked out without rewriting anyone's stored
// email. Both forms are still email lookups, so neither can match
// someone else's account: authentication still needs that account's
// password.
func loginEmailCandidates(typed string) []string {
	typed = strings.ToLower(strings.TrimSpace(typed))
	if typed == "" {
		return nil
	}
	normalized := normalizeEmail(typed)
	if normalized == typed {
		return []string{normalized}
	}
	return []string{normalized, typed}
}

// isReservedUsername reports whether a display name is the owner
// portal's login name (OWNER_USERNAME, default "owner").
//
// Reserved because handleToggleUser (account.go) refuses to deactivate
// any account whose name equals it -- a guard meant to protect an owner
// account. The owner actually signs in with HTTP Basic Auth from
// environment variables, not through a users row, so in practice that
// guard protects whoever registered the name. While usernames were
// unique that was at most one stray account; with names free to repeat,
// anyone could register as "owner" and become impossible to block. So the
// name can't be claimed at signup.
func isReservedUsername(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(ownerUsername))
}
