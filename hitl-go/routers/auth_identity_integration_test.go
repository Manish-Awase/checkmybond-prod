//go:build integration

// End-to-end check of the identity rules, through the REAL register and
// login handlers and a REAL Postgres + Redis -- not mocks.
//
// Opt-in, because it needs infrastructure:
//
//	IDENTITY_TEST_DATABASE_URL   a Postgres with the app's schema, migration
//	                             0006 applied (or built fresh from the model)
//	REDIS_HOST                   a reachable Redis (default localhost)
//	JWT_PRIVATE_KEY_PATH         } the same RSA key pair the server uses
//	JWT_PUBLIC_KEY_PATH          }
//
//	go test -tags integration ./routers/ -run TestIdentity -v
//
// It only ever writes rows whose email carries a per-run marker, and
// deletes exactly those afterwards, so it is safe to point at a database
// that has other data in it.
package routers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	iauth "hitl-platform/internal/auth"
	idb "hitl-platform/internal/db"
	iqueue "hitl-platform/internal/queue"
)

func idCall(t *testing.T, h http.Handler, path string, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func TestIdentity(t *testing.T) {
	dsn := os.Getenv("IDENTITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("IDENTITY_TEST_DATABASE_URL not set")
	}
	os.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	if err := idb.Init(ctx); err != nil {
		t.Fatalf("db init: %v", err)
	}
	iqueue.InitBlacklist()

	ownerUsername = "owner"
	h := AuthRouter()

	// Every email this run creates contains the marker, so cleanup can't
	// touch anyone else's rows.
	run := fmt.Sprintf("idt%d", time.Now().UnixNano()%1_000_000_000)
	email := func(local string) string { return fmt.Sprintf("%s.%s@example.com", local, run) }
	t.Cleanup(func() {
		_, _ = idb.Pool().Exec(ctx, `DELETE FROM users WHERE lower(email) LIKE $1`, "%"+run+"%")
	})

	// ── Two different people, the SAME name ──────────────────────────────
	code, alice := idCall(t, h, "/register", map[string]any{"username": "John Smith", "email": email("alice"), "password": "alice-password-1"})
	if code != http.StatusCreated {
		t.Fatalf("register alice: %d %v", code, alice)
	}
	code, bob := idCall(t, h, "/register", map[string]any{"username": "John Smith", "email": email("bob"), "password": "bob-password-22"})
	if code != http.StatusCreated {
		t.Fatalf("a second account with the same name must be allowed, got %d %v", code, bob)
	}
	if str(alice, "user_id") == str(bob, "user_id") {
		t.Fatal("two accounts share a user_id")
	}
	if str(alice, "username") != str(bob, "username") {
		t.Fatalf("test setup: names should match: %q vs %q", str(alice, "username"), str(bob, "username"))
	}
	t.Logf("ok: two accounts named %q registered", str(alice, "username"))

	// Each signs in with their OWN email and lands in their OWN account --
	// the thing a username-based lookup could not guarantee.
	code, a := idCall(t, h, "/login", map[string]any{"login": email("alice"), "password": "alice-password-1"})
	if code != http.StatusOK || str(a, "user_id") != str(alice, "user_id") {
		t.Fatalf("alice login: %d %v (want user_id %s)", code, a, str(alice, "user_id"))
	}
	code, b := idCall(t, h, "/login", map[string]any{"login": email("bob"), "password": "bob-password-22"})
	if code != http.StatusOK || str(b, "user_id") != str(bob, "user_id") {
		t.Fatalf("bob login: %d %v (want user_id %s)", code, b, str(bob, "user_id"))
	}
	t.Log("ok: each same-named user signs in to their own account by email")

	// The accounts are separate: one person's password doesn't open the other's.
	if code, _ = idCall(t, h, "/login", map[string]any{"login": email("bob"), "password": "alice-password-1"}); code != http.StatusUnauthorized {
		t.Fatalf("bob with alice's password: %d, want 401", code)
	}
	// A display name is not a login.
	if code, _ = idCall(t, h, "/login", map[string]any{"login": "john smith", "password": "alice-password-1"}); code != http.StatusUnauthorized {
		t.Fatalf("login by name: %d, want 401", code)
	}
	t.Log("ok: a name cannot be used to sign in")

	// Email case: normalization means typing it differently still works.
	if code, a = idCall(t, h, "/login", map[string]any{"login": "  " + email("ALICE") + " ", "password": "alice-password-1"}); code != http.StatusOK || str(a, "user_id") != str(alice, "user_id") {
		t.Fatalf("login with different case/spacing: %d %v", code, a)
	}

	// ── Email is the one thing that must be unique ───────────────────────
	for label, e := range map[string]string{"exact": email("alice"), "different case": email("ALICE")} {
		code, r := idCall(t, h, "/register", map[string]any{"username": "Someone Else", "email": e, "password": "another-password"})
		if code != http.StatusConflict || str(r, "detail") != "email already registered" {
			t.Fatalf("duplicate email (%s): %d %v, want 409 email already registered", label, code, r)
		}
	}
	t.Log("ok: a duplicate email is rejected (exact and different case)")

	// Gmail dot/plus variants are the same mailbox.
	gm := fmt.Sprintf("%s@gmail.com", run)
	if code, r := idCall(t, h, "/register", map[string]any{"username": "gm one", "email": gm, "password": "gmail-password-1"}); code != http.StatusCreated {
		t.Fatalf("gmail register: %d %v", code, r)
	}
	dotted := fmt.Sprintf("%s+promo@gmail.com", run)
	if code, r := idCall(t, h, "/register", map[string]any{"username": "gm two", "email": dotted, "password": "gmail-password-2"}); code != http.StatusConflict {
		t.Fatalf("gmail +alias of an existing address: %d %v, want 409", code, r)
	}
	t.Log("ok: Gmail +alias variants are still one address")

	// ── The race: the database, not the pre-check, is the guarantee ──────
	raceEmail := email("race")
	var wg sync.WaitGroup
	var mu sync.Mutex
	counts := map[int]int{}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _ := idCall(t, h, "/register", map[string]any{"username": "racer", "email": raceEmail, "password": "race-password-1"})
			mu.Lock()
			counts[code]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if counts[http.StatusCreated] != 1 || counts[http.StatusConflict] != 9 || len(counts) != 2 {
		t.Fatalf("10 simultaneous signups with one email: %v, want exactly one 201 and nine 409 (a 500 means the duplicate wasn't mapped)", counts)
	}
	t.Logf("ok: 10 simultaneous signups with one email -> %v", counts)

	// ── Validation: email is the identity, so it must be a real address ──
	for _, bad := range []string{"", "notanemail", "a@b", "Jane <j@example.com>"} {
		if code, r := idCall(t, h, "/register", map[string]any{"username": "valid name", "email": bad, "password": "long-enough-pw"}); code != http.StatusUnprocessableEntity {
			t.Fatalf("register with email %q: %d %v, want 422", bad, code, r)
		}
	}
	// ...and the one reserved name.
	for _, n := range []string{"owner", "OWNER", " Owner "} {
		if code, r := idCall(t, h, "/register", map[string]any{"username": n, "email": email("own" + fmt.Sprint(len(n))), "password": "long-enough-pw"}); code != http.StatusUnprocessableEntity {
			t.Fatalf("register as %q: %d %v, want 422", n, code, r)
		}
	}
	t.Log("ok: invalid emails and the reserved owner name are rejected")

	// ── Legacy rows: accounts stored before emails were normalized ───────
	hash, _ := iauth.HashPassword("legacy-password-1")
	legacyMixed := fmt.Sprintf("Legacy.User.%s@Example.com", run)
	legacyDots := fmt.Sprintf("l.e.g.%s@gmail.com", run) // would normalize to "leg<run>@gmail.com"
	for _, e := range []string{legacyMixed, legacyDots} {
		if _, err := idb.Pool().Exec(ctx,
			`INSERT INTO users (id, username, email, password_hash, role, is_active, created_at, token_balance)
			 VALUES ($1,$2,$3,$4,'analyst',true,now(),0)`, uuid.New().String(), "legacy", e, hash); err != nil {
			t.Fatalf("insert legacy %q: %v", e, err)
		}
	}
	// They used to get in by username; now they must still get in by email.
	for _, typed := range []string{legacyMixed, "legacy.user." + run + "@example.com", legacyDots} {
		if code, r := idCall(t, h, "/login", map[string]any{"login": typed, "password": "legacy-password-1"}); code != http.StatusOK {
			t.Fatalf("legacy login as %q: %d %v -- this would lock the account out", typed, code, r)
		}
	}
	t.Log("ok: accounts with older-style stored emails can still sign in")
}

// The register handler's pre-check is only a courtesy; the database's
// unique index is the real guarantee. This hits that layer directly, so
// the ErrEmailTaken mapping is tested on its own rather than being
// masked by the pre-check in the handler tests above.
func TestIdentityDBLayer(t *testing.T) {
	dsn := os.Getenv("IDENTITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("IDENTITY_TEST_DATABASE_URL not set")
	}
	os.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	if err := idb.Init(ctx); err != nil {
		t.Fatalf("db init: %v", err)
	}
	run := fmt.Sprintf("dbl%d", time.Now().UnixNano()%1_000_000_000)
	t.Cleanup(func() {
		_, _ = idb.Pool().Exec(ctx, `DELETE FROM users WHERE lower(email) LIKE $1`, "%"+run+"%")
	})
	mk := func(name, email string) error {
		_, err := idb.CreateUser(ctx, uuid.New().String(), name, email, "hash", "analyst")
		return err
	}

	if err := mk("same name", "first."+run+"@example.com"); err != nil {
		t.Fatal(err)
	}
	// Same NAME, different email: allowed.
	if err := mk("same name", "second."+run+"@example.com"); err != nil {
		t.Fatalf("same name with a different email must be allowed: %v", err)
	}
	// Same email -- exact, and in a different case: both map to ErrEmailTaken.
	for _, e := range []string{"first." + run + "@example.com", "FIRST." + run + "@Example.com"} {
		if err := mk("anyone", e); !errors.Is(err, idb.ErrEmailTaken) {
			t.Fatalf("CreateUser(%q) = %v, want ErrEmailTaken", e, err)
		}
	}
	// Lookup ignores case and finds exactly the one account.
	u, err := idb.GetUserByEmail(ctx, "FIRST."+run+"@EXAMPLE.com")
	if err != nil || u == nil || u.Email != "first."+run+"@example.com" {
		t.Fatalf("GetUserByEmail case-insensitive: %v %+v", err, u)
	}
	if u, _ := idb.GetUserByEmail(ctx, "nobody."+run+"@example.com"); u != nil {
		t.Fatalf("unknown email matched %+v", u)
	}
}

// idCallAs is idCall with extra headers. The gateway is what normally sets
// X-User-Id / X-User-Role after verifying a login; the server trusts them,
// so a test can stand in for the gateway by setting them directly.
func idCallAs(t *testing.T, h http.Handler, path string, body map[string]any, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// Public signup must NEVER hand out a privileged role, whatever the
// request asks for -- it used to trust the "role" in the request body, so a
// stranger could register as admin. And only an admin may create accounts
// with a chosen role.
func TestIdentityRoles(t *testing.T) {
	dsn := os.Getenv("IDENTITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("IDENTITY_TEST_DATABASE_URL not set")
	}
	os.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	if err := idb.Init(ctx); err != nil {
		t.Fatalf("db init: %v", err)
	}
	iqueue.InitBlacklist()
	ownerUsername = "owner"
	h := AuthRouter()

	run := fmt.Sprintf("rol%d", time.Now().UnixNano()%1_000_000_000)
	email := func(local string) string { return fmt.Sprintf("%s.%s@example.com", local, run) }
	t.Cleanup(func() {
		_, _ = idb.Pool().Exec(ctx, `DELETE FROM users WHERE lower(email) LIKE $1`, "%"+run+"%")
	})
	roleInDB := func(e string) string {
		var r string
		_ = idb.Pool().QueryRow(ctx, `SELECT role FROM users WHERE lower(email)=lower($1)`, e).Scan(&r)
		return r
	}

	// ── Public signup ignores the requested role ─────────────────────────
	for i, asked := range []string{"admin", "debug", "reviewer", "ADMIN", "superuser", ""} {
		e := email(fmt.Sprintf("pub%d", i)) // distinct addresses: "pub-admin" vs "pub-ADMIN" would collide (emails ignore case)
		code, r := idCall(t, h, "/register", map[string]any{"username": "public user", "email": e, "password": "long-enough-pw", "role": asked})
		if code != http.StatusCreated {
			t.Fatalf("public register asking for role %q: %d %v", asked, code, r)
		}
		if str(r, "role") != "analyst" || roleInDB(e) != "analyst" {
			t.Fatalf("SECURITY: public signup asking for role %q produced role %q (db: %q) -- must always be analyst", asked, str(r, "role"), roleInDB(e))
		}
	}
	t.Log("ok: public signup always creates an analyst, whatever role is requested")

	// ── Admin-only account creation ──────────────────────────────────────
	body := func(local, role string) map[string]any {
		return map[string]any{"username": "created by admin", "email": email(local), "password": "long-enough-pw", "role": role}
	}
	admin := map[string]string{"X-User-Id": "admin-1", "X-User-Role": "admin"}
	analyst := map[string]string{"X-User-Id": "user-1", "X-User-Role": "analyst"}

	if code, _ := idCallAs(t, h, "/users", body("nohdr", "admin"), nil); code != http.StatusUnauthorized {
		t.Fatalf("no login at all: %d, want 401", code)
	}
	for _, role := range []string{"analyst", "reviewer", "debug"} {
		hdr := map[string]string{"X-User-Id": "u", "X-User-Role": role}
		if code, _ := idCallAs(t, h, "/users", body("deny-"+role, "admin"), hdr); code != http.StatusForbidden {
			t.Fatalf("a %s creating an admin: %d, want 403", role, code)
		}
	}
	if got := roleInDB(email("deny-analyst")); got != "" {
		t.Fatalf("a refused request still created an account (role %q)", got)
	}
	_ = analyst
	t.Log("ok: only an admin can reach the create-account endpoint (401 without login, 403 for other roles)")

	for _, role := range []string{"reviewer", "admin", "debug", "analyst"} {
		code, r := idCallAs(t, h, "/users", body("ok-"+role, role), admin)
		if code != http.StatusCreated || str(r, "role") != role || roleInDB(email("ok-"+role)) != role {
			t.Fatalf("admin creating a %s: %d %v (db role %q)", role, code, r, roleInDB(email("ok-"+role)))
		}
		if _, has := r["access_token"]; has {
			t.Fatal("admin create-user must not return a session token for the new account")
		}
	}
	// no role given -> least privilege
	if code, r := idCallAs(t, h, "/users", map[string]any{"username": "no role given", "email": email("norole"), "password": "long-enough-pw"}, admin); code != http.StatusCreated || str(r, "role") != "analyst" {
		t.Fatalf("admin create with no role: %d %v, want analyst", code, r)
	}
	if code, r := idCallAs(t, h, "/users", body("badrole", "superuser"), admin); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown role: %d %v, want 422", code, r)
	}
	if code, _ := idCallAs(t, h, "/users", body("ok-admin", "reviewer"), admin); code != http.StatusConflict {
		t.Fatalf("duplicate email via the admin endpoint: %d, want 409", code)
	}
	t.Log("ok: an admin can create reviewer/admin/debug/analyst accounts; unknown roles and duplicate emails are rejected")
}
