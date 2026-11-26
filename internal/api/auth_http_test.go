package api

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLoginAndMe(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	me := h.expect(c, http.MethodGet, "/api/v1/me", nil, http.StatusOK)
	if me["username"] != "admin" || me["role"] != "admin" {
		t.Fatalf("unexpected identity: %v", me)
	}
	if me["org_name"] == "" {
		t.Fatal("org name missing from /me")
	}
	if _, leaked := me["password_hash"]; leaked {
		t.Fatal("password hash exposed")
	}
}

func TestLoginFailures(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	status, body := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "admin", "password": "wrong"})
	if status != http.StatusUnauthorized {
		t.Fatalf("bad password = %d, want 401 (%s)", status, body)
	}
	status, body = h.do(c, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "ghost", "password": "whatever"})
	if status != http.StatusUnauthorized {
		t.Fatalf("unknown user = %d, want 401 (%s)", status, body)
	}
	if strings.Contains(string(body), "ghost") {
		t.Fatal("response reveals whether the user exists")
	}
}

func TestDisabledAccountCannotLogIn(t *testing.T) {
	h := newHarness(t)
	admin := h.login()
	h.expect(admin, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "temp", "password": "another-long-password", "role": "operator",
	}, http.StatusCreated)
	h.expect(admin, http.MethodPut, "/api/v1/users/2", map[string]any{"disabled": true}, http.StatusOK)

	c := h.client()
	status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "temp", "password": "another-long-password"})
	if status != http.StatusUnauthorized {
		t.Fatalf("disabled account logged in: %d", status)
	}
}

func TestLoginRateLimit(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	var last int
	for i := 0; i < 12; i++ {
		last, _ = h.do(c, http.MethodPost, h.admin.URL+"/api/v1/login",
			map[string]string{"username": "admin", "password": "wrong"})
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after repeated failures the status was %d, want 429", last)
	}
}

func TestUnauthenticatedAccessIsRejected(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	for _, path := range []string{"/api/v1/me", "/api/v1/campaigns", "/api/v1/groups", "/api/v1/users", "/api/v1/audit"} {
		if status, _ := h.do(c, http.MethodGet, h.admin.URL+path, nil); status != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401", path, status)
		}
	}
	// A forged cookie must not work either.
	req, _ := http.NewRequest(http.MethodGet, h.admin.URL+"/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: "forged-token"})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged cookie accepted: %d", resp.StatusCode)
	}
}

func TestRoleEnforcement(t *testing.T) {
	h := newHarness(t)
	admin := h.login()
	for _, u := range []map[string]any{
		{"username": "viewer1", "password": "viewer-password-1", "role": "viewer"},
		{"username": "operator1", "password": "operator-password-1", "role": "operator"},
	} {
		h.expect(admin, http.MethodPost, "/api/v1/users", u, http.StatusCreated)
	}

	viewer := h.client()
	if status, body := h.do(viewer, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "viewer1", "password": "viewer-password-1"}); status != http.StatusOK {
		t.Fatalf("viewer login failed: %d %s", status, body)
	}
	if status, _ := h.do(viewer, http.MethodGet, h.admin.URL+"/api/v1/groups", nil); status != http.StatusOK {
		t.Fatalf("viewer cannot read groups: %d", status)
	}
	if status, _ := h.do(viewer, http.MethodPost, h.admin.URL+"/api/v1/groups",
		map[string]any{"name": "x", "targets": []any{}}); status != http.StatusForbidden {
		t.Fatalf("viewer created a group: %d", status)
	}
	if status, _ := h.do(viewer, http.MethodGet, h.admin.URL+"/api/v1/users", nil); status != http.StatusForbidden {
		t.Fatalf("viewer listed users: %d", status)
	}

	operator := h.client()
	h.do(operator, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "operator1", "password": "operator-password-1"})
	if status, _ := h.do(operator, http.MethodPost, h.admin.URL+"/api/v1/groups",
		map[string]any{"name": "ops", "targets": []any{}}); status != http.StatusCreated {
		t.Fatalf("operator could not create a group: %d", status)
	}
	if status, _ := h.do(operator, http.MethodGet, h.admin.URL+"/api/v1/users", nil); status != http.StatusForbidden {
		t.Fatalf("operator listed users: %d", status)
	}
}

func TestOriginGuard(t *testing.T) {
	h := newHarness(t)
	c := h.login()

	send := func(origin string) int {
		req, _ := http.NewRequest(http.MethodPost, h.admin.URL+"/api/v1/groups",
			strings.NewReader(`{"name":"x","targets":[]}`))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if got := send("https://evil.example"); got != http.StatusForbidden {
		t.Fatalf("cross-origin POST = %d, want 403", got)
	}
	if got := send(h.admin.URL); got != http.StatusCreated {
		t.Fatalf("same-origin POST = %d, want 201", got)
	}
	if got := send("https://console.test"); got != http.StatusCreated {
		t.Fatalf("trusted origin POST = %d, want 201", got)
	}
}

func TestAPIKeyAuthentication(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	key := h.expect(c, http.MethodPost, "/api/v1/me/apikey", nil, http.StatusOK)["api_key"].(string)
	if len(key) < 20 {
		t.Fatalf("API key looks weak: %q", key)
	}

	anon := h.client()
	req, _ := http.NewRequest(http.MethodGet, h.admin.URL+"/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := anon.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("API key rejected: %d", resp.StatusCode)
	}

	// Keys are stored hashed, so the plaintext must not appear in the database.
	var stored string
	if err := h.st.DB().QueryRow(`SELECT api_key_hash FROM users WHERE username = 'admin'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == key || strings.Contains(stored, key) {
		t.Fatal("API key stored in clear text")
	}

	// Rotating invalidates the old key.
	h.expect(c, http.MethodPost, "/api/v1/me/apikey", nil, http.StatusOK)
	resp2, _ := anon.Do(req)
	if resp2 != nil {
		resp2.Body.Close()
		if resp2.StatusCode != http.StatusUnauthorized {
			t.Fatalf("rotated key still works: %d", resp2.StatusCode)
		}
	}
}

func TestPasswordChangeRotatesSession(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.expect(c, http.MethodPost, "/api/v1/me/password", map[string]string{
		"current": testPassword, "new": "a-brand-new-password",
	}, http.StatusOK)

	// The response set a fresh cookie, so this client is still signed in.
	if status, _ := h.do(c, http.MethodGet, h.admin.URL+"/api/v1/me", nil); status != http.StatusOK {
		t.Fatalf("session lost after password change: %d", status)
	}
	// The old password no longer works; the new one does.
	fresh := h.client()
	if status, _ := h.do(fresh, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "admin", "password": testPassword}); status != http.StatusUnauthorized {
		t.Fatal("old password still accepted")
	}
	if status, body := h.do(fresh, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "admin", "password": "a-brand-new-password"}); status != http.StatusOK {
		t.Fatalf("new password rejected: %d %s", status, body)
	}
	// Short passwords are refused.
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/me/password", map[string]string{
		"current": "a-brand-new-password", "new": "short",
	}); status != http.StatusBadRequest {
		t.Fatalf("short password accepted: %d", status)
	}
}

func TestTOTPFlow(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	setup := h.expect(c, http.MethodPost, "/api/v1/me/totp/setup", nil, http.StatusOK)
	secret, _ := setup["secret"].(string)
	if secret == "" || !strings.HasPrefix(setup["url"].(string), "otpauth://totp/") {
		t.Fatalf("unexpected setup response: %v", setup)
	}

	// Wrong code does not enable 2FA.
	h.expect(c, http.MethodPost, "/api/v1/me/totp/enable", map[string]string{"code": "000000"}, http.StatusBadRequest)
	h.expect(c, http.MethodPost, "/api/v1/me/totp/enable", map[string]string{"code": totpNow(secret)}, http.StatusOK)

	// Login now needs the code.
	anon := h.client()
	status, body := h.do(anon, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "admin", "password": testPassword})
	if status != http.StatusUnauthorized {
		t.Fatalf("login without TOTP = %d, want 401 (%s)", status, body)
	}
	status, body = h.do(anon, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "admin", "password": testPassword, "totp": totpNow(secret)})
	if status != http.StatusOK {
		t.Fatalf("login with TOTP = %d (%s)", status, body)
	}
}

func TestLastAdminIsProtected(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.expect(c, http.MethodPut, "/api/v1/users/1", map[string]any{"role": "operator"}, http.StatusBadRequest)
	h.expect(c, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "self", "password": "a-long-enough-password", "role": "admin",
	}, http.StatusCreated)
	// Now demotion of the original admin is allowed, but self-delete never is.
	h.expect(c, http.MethodDelete, "/api/v1/users/1", nil, http.StatusBadRequest)
}

func TestAuditLogRecordsActions(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.seedCampaign(t, c)
	entries := h.expectRaw(c, http.MethodGet, "/api/v1/audit", nil, http.StatusOK)
	if !strings.Contains(string(entries), "login.success") {
		t.Fatalf("login not audited: %s", entries)
	}
	if !strings.Contains(string(entries), "campaign.saved") {
		t.Fatalf("campaign creation not audited: %s", entries)
	}
	if strings.Contains(string(entries), testPassword) {
		t.Fatal("audit log leaks a password")
	}
}

// helpers ------------------------------------------------------------------

func totpNow(secret string) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).
		DecodeString(strings.ToUpper(secret))
	if err != nil {
		panic(err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(time.Now().Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (int64(sum[offset])&0x7f)<<24 |
		int64(sum[offset+1])<<16 |
		int64(sum[offset+2])<<8 |
		int64(sum[offset+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}
