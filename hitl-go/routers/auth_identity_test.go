package routers

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidEmail(t *testing.T) {
	good := []string{
		"a@b.co", "jane.doe@example.com", "jdoe+tag@gmail.com", "x_y-z@sub.example.org",
		strings.Repeat("a", 60) + "@example.com",
	}
	for _, e := range good {
		if !validEmail(e) {
			t.Errorf("validEmail(%q) = false, want true", e)
		}
	}

	bad := map[string]string{
		"":                                 "empty",
		"plainstring":                      "no @",
		"@example.com":                     "no local part",
		"a@":                               "no domain",
		"a@localhost":                      "domain without a dot",
		"a@b":                              "domain without a dot",
		"Jane <jane@example.com>":          "display-name form must be rejected, not stored",
		"a@b.co, c@d.co":                   "two addresses",
		"a b@example.com":                  "space in local part",
		"a@example.com ":                   "trailing space (callers trim first)",
		strings.Repeat("a", 250) + "@b.co": "longer than 254",
	}
	for e, why := range bad {
		if validEmail(e) {
			t.Errorf("validEmail(%q) = true, want false (%s)", e, why)
		}
	}
}

func TestLoginEmailCandidates(t *testing.T) {
	cases := []struct {
		typed string
		want  []string
	}{
		// A plain address: one candidate.
		{"jane@example.com", []string{"jane@example.com"}},
		// Case and surrounding space are ignored.
		{"  Jane@Example.COM ", []string{"jane@example.com"}},
		// Gmail: normalized form first, as typed second -- the second is
		// what rescues a legacy row stored before normalization existed.
		{"j.doe@gmail.com", []string{"jdoe@gmail.com", "j.doe@gmail.com"}},
		{"jdoe+promo@gmail.com", []string{"jdoe@gmail.com", "jdoe+promo@gmail.com"}},
		// Already normalized Gmail: no duplicate candidate.
		{"jdoe@gmail.com", []string{"jdoe@gmail.com"}},
		// Nothing usable.
		{"", nil},
		{"   ", nil},
	}
	for _, c := range cases {
		got := loginEmailCandidates(c.typed)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("loginEmailCandidates(%q) = %v, want %v", c.typed, got, c.want)
		}
	}
}

// A username is not an email, so it must never be treated as one: typing a
// display name at the login box has to produce a candidate that can only
// ever match an account whose EMAIL is literally that string.
func TestLoginCandidatesNeverInventAnAddress(t *testing.T) {
	got := loginEmailCandidates("john")
	if !reflect.DeepEqual(got, []string{"john"}) {
		t.Fatalf("got %v", got)
	}
	// ...and since registration rejects "john" as an email (validEmail),
	// no account can have that email, so such a login can never succeed.
	if validEmail("john") {
		t.Fatal("a bare name must not be a valid email, or username-style logins could resolve to an account")
	}
}

func TestIsReservedUsername(t *testing.T) {
	prev := ownerUsername
	ownerUsername = "owner"
	defer func() { ownerUsername = prev }()

	for _, n := range []string{"owner", "OWNER", "Owner", "  owner  "} {
		if !isReservedUsername(n) {
			t.Errorf("isReservedUsername(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"owners", "own", "co-owner", "ownerx", "john", ""} {
		if isReservedUsername(n) {
			t.Errorf("isReservedUsername(%q) = true, want false", n)
		}
	}

	// Follows the configured owner name, not a hard-coded one.
	ownerUsername = "Boss"
	if !isReservedUsername("boss") || isReservedUsername("owner") {
		t.Error("should track ownerUsername")
	}
}
