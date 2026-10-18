package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"nexos/hub/auth"
	"nexos/hub/events"
	"nexos/hub/store"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: base, http: &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrf)
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	var any_ any
	if json.NewDecoder(resp.Body).Decode(&any_) == nil {
		if m, ok := any_.(map[string]any); ok {
			out = m
		} else {
			out = map[string]any{"list": any_}
		}
	}
	if tok, ok := out["csrf_token"].(string); ok {
		c.csrf = tok
	}
	return resp.StatusCode, out
}

// code returns a TOTP code for the next time step, so consecutive logins
// in one test do not collide with replay protection.
func code(t *testing.T, secret string, step int) string {
	t.Helper()
	c, err := totp.GenerateCodeCustom(secret, time.Now().Add(time.Duration(step)*30*time.Second), totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func setupServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sealer, err := auth.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{SetupToken: "setup-token"}, st, nil, events.NewBus(), sealer, nil, nil, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, st
}

// createVia completes a TOTP setup flow (first-run or invite) and returns
// the TOTP secret.
func completeSetup(t *testing.T, c *client, beginPath string, beginBody map[string]any, completePath string) string {
	t.Helper()
	st, b := c.do("POST", beginPath, beginBody)
	if st != 200 {
		t.Fatalf("%s: %d %v", beginPath, st, b)
	}
	secret := b["totp_secret"].(string)
	if st, b := c.do("POST", completePath, map[string]any{"setup_id": b["setup_id"], "code": code(t, secret, 0)}); st != 200 {
		t.Fatalf("%s: %d %v", completePath, st, b)
	}
	return secret
}

func TestUserLifecycle(t *testing.T) {
	srv, st := setupServer(t)
	admin := newClient(t, srv.URL)
	completeSetup(t, admin, "/api/setup/begin", map[string]any{"setup_token": "setup-token", "username": "admin", "password": "correct horse battery"}, "/api/setup/complete")

	// Invite a viewer.
	s, b := admin.do("POST", "/api/users", map[string]any{"username": "olek", "role": "viewer"})
	if s != 201 {
		t.Fatalf("create user: %d %v", s, b)
	}
	invite := b["invite_token"].(string)
	uid := int64(b["id"].(float64))
	if s, _ := admin.do("POST", "/api/users", map[string]any{"username": "OLEK", "role": "viewer"}); s != 409 {
		t.Fatalf("duplicate username (case-insensitive): %d", s)
	}

	olek := newClient(t, srv.URL)
	if s, b := olek.do("GET", "/api/invite/"+invite, nil); s != 200 || b["username"] != "olek" {
		t.Fatalf("invite info: %d %v", s, b)
	}
	if s, _ := olek.do("POST", "/api/invite/begin", map[string]any{"token": invite, "password": "short"}); s != 400 {
		t.Fatalf("weak password accepted: %d", s)
	}
	// An invite setup id must not complete the first-run setup (privilege escalation).
	_, ib := olek.do("POST", "/api/invite/begin", map[string]any{"token": invite, "password": "olek password 123"})
	if s, _ := olek.do("POST", "/api/setup/complete", map[string]any{"setup_id": ib["setup_id"], "code": code(t, ib["totp_secret"].(string), 0)}); s == 200 {
		t.Fatal("invite setup completed through first-run setup endpoint")
	}
	olekSecret := completeSetup(t, olek, "/api/invite/begin", map[string]any{"token": invite, "password": "olek password 123"}, "/api/invite/complete")
	if s, _ := newClient(t, srv.URL).do("GET", "/api/invite/"+invite, nil); s != 404 {
		t.Fatalf("invite reusable: %d", s)
	}

	// Viewer permissions.
	if s, _ := olek.do("GET", "/api/hosts", nil); s != 200 {
		t.Fatalf("viewer cannot list hosts: %d", s)
	}
	if s, _ := olek.do("GET", "/api/users", nil); s != 403 {
		t.Fatalf("viewer can list users: %d", s)
	}
	if s, _ := olek.do("POST", "/api/hosts", map[string]any{"name": "x", "address": "1.2.3.4:1"}); s != 403 {
		t.Fatalf("viewer can create hosts: %d", s)
	}

	// Admin cannot modify own account through the admin API.
	me := int64(1)
	if s, _ := admin.do("PATCH", "/api/users/1", map[string]any{"role": "viewer"}); s != 409 {
		t.Fatalf("self demotion: %d", s)
	}
	_ = me
	if s, _ := admin.do("PATCH", "/api/users/"+itoa(uid), map[string]any{"role": "operator"}); s != 204 {
		t.Fatalf("role change: %d", s)
	}

	// Password change needs current password and code; revokes other sessions.
	// A second device: TOTP replay protection allows each time step only
	// once, so create the session directly instead of logging in again.
	olek2 := newClient(t, srv.URL)
	now := time.Now()
	if err := st.CreateSession(context.Background(), "second-device-token", store.Session{UserID: uid, CSRFToken: "x", CreatedAt: now, ExpiresAt: now.Add(time.Hour), LastSeenAt: now}, "", ""); err != nil {
		t.Fatal(err)
	}
	u, _ := http.NewRequest("GET", srv.URL, nil)
	olek2.http.Jar.SetCookies(u.URL, []*http.Cookie{{Name: sessionCookie, Value: "second-device-token", Path: "/"}})
	if s, _ := olek2.do("GET", "/api/auth/me", nil); s != 200 {
		t.Fatalf("second session: %d", s)
	}
	if s, _ := olek.do("POST", "/api/account/password", map[string]any{"current_password": "wrong", "new_password": "new olek password", "code": code(t, olekSecret, 1)}); s != 403 {
		t.Fatalf("password change with wrong password: %d", s)
	}
	if s, b := olek.do("POST", "/api/account/password", map[string]any{"current_password": "olek password 123", "new_password": "new olek password", "code": code(t, olekSecret, 1)}); s != 204 {
		t.Fatalf("password change: %d %v", s, b)
	}
	if s, _ := olek2.do("GET", "/api/auth/me", nil); s != 401 {
		t.Fatalf("other session survived password change: %d", s)
	}
	if s, _ := olek.do("GET", "/api/auth/me", nil); s != 200 {
		t.Fatalf("current session ended by password change: %d", s)
	}

	// Login without a code and without passkeys fails.
	if s, _ := newClient(t, srv.URL).do("POST", "/api/auth/login", map[string]any{"username": "olek", "password": "new olek password"}); s != 401 {
		t.Fatalf("login without second factor: %d", s)
	}

	// Reset ends sessions and requires a new invite.
	s, b = admin.do("POST", "/api/users/"+itoa(uid)+"/reset", nil)
	if s != 200 || b["invite_token"] == nil {
		t.Fatalf("reset: %d %v", s, b)
	}
	if s, _ := olek.do("GET", "/api/auth/me", nil); s != 401 {
		t.Fatalf("session survived reset: %d", s)
	}
	if s, _ := newClient(t, srv.URL).do("POST", "/api/auth/login", map[string]any{"username": "olek", "password": "new olek password", "code": code(t, olekSecret, 1)}); s != 401 {
		t.Fatalf("login after reset: %d", s)
	}

	// The last admin cannot be demoted.
	if err := st.SetUserRole(context.Background(), 1, "viewer"); err != store.ErrLastAdmin {
		t.Fatalf("last admin demoted: %v", err)
	}
	if s, _ := admin.do("DELETE", "/api/users/"+itoa(uid), nil); s != 204 {
		t.Fatalf("delete user: %d", s)
	}
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
