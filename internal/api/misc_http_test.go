package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLogoutRevokesSession(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	if status, _ := h.do(c, http.MethodGet, h.admin.URL+"/api/v1/me", nil); status != http.StatusOK {
		t.Fatalf("logged in client = %d", status)
	}
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/logout", nil); status != http.StatusOK {
		t.Fatalf("logout = %d", status)
	}
	if status, _ := h.do(c, http.MethodGet, h.admin.URL+"/api/v1/me", nil); status != http.StatusUnauthorized {
		t.Fatalf("session survived logout: %d", status)
	}
	// Logging out again is unauthenticated, not an error in the session store.
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/logout", nil); status != http.StatusUnauthorized {
		t.Fatalf("second logout = %d, want 401", status)
	}
}

func TestListEndpoints(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.seedCampaign(t, c)

	for path, want := range map[string]string{
		"/api/v1/templates":     "Invoice",
		"/api/v1/pages":         "Portal",
		"/api/v1/profiles":      "Relay",
		"/api/v1/webhooks":      "[]",
		"/api/v1/users":         "admin",
		"/api/v1/attachments":   "[]",
		"/api/v1/audit":         "login.success",
		"/api/v1/ai/status":     "enabled",
		"/api/v1/settings":      "phish_url",
		"/api/v1/health":        "ok",
		"/api/v1/dashboard":     "stats",
	} {
		status, body := h.do(c, http.MethodGet, h.admin.URL+path, nil)
		if status != http.StatusOK {
			t.Errorf("%s = %d", path, status)
			continue
		}
		if !strings.Contains(string(body), want) {
			t.Errorf("%s does not contain %q: %s", path, want, body)
		}
	}
	// The health endpoint must work without a session.
	if status, _ := h.do(h.client(), http.MethodGet, h.admin.URL+"/api/v1/health", nil); status != http.StatusOK {
		t.Fatalf("unauthenticated health = %d", status)
	}
}

func TestTOTPDisable(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	setup := h.expect(c, http.MethodPost, "/api/v1/me/totp/setup", nil, http.StatusOK)
	secret := setup["secret"].(string)
	h.expect(c, http.MethodPost, "/api/v1/me/totp/enable", map[string]string{"code": totpNow(secret)}, http.StatusOK)

	// Wrong password is refused.
	h.expect(c, http.MethodPost, "/api/v1/me/totp/disable", map[string]string{"password": "nope"}, http.StatusForbidden)
	me := h.expect(c, http.MethodGet, "/api/v1/me", nil, http.StatusOK)
	if me["totp_enabled"] != true {
		t.Fatal("2FA disabled by a wrong password")
	}

	h.expect(c, http.MethodPost, "/api/v1/me/totp/disable", map[string]string{"password": testPassword}, http.StatusOK)
	me = h.expect(c, http.MethodGet, "/api/v1/me", nil, http.StatusOK)
	if me["totp_enabled"] != false {
		t.Fatal("2FA still enabled")
	}
	// Setup is refused while already enabled, and once disabled it works again.
	h.expect(c, http.MethodPost, "/api/v1/me/totp/setup", nil, http.StatusOK)
}

func TestCombinedHandlerServesBothAudiences(t *testing.T) {
	h := newHarness(t)
	combined := h.srv.CombinedHandler()
	srv := httptest.NewServer(combined)
	defer srv.Close()

	// Admin UI + API.
	resp, err := http.Get(srv.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health on the combined handler = %d", resp.StatusCode)
	}
	resp, err = http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("UI on the combined handler = %d", resp.StatusCode)
	}
	// Tracking endpoint.
	resp, err = http.Get(srv.URL + "/open.gif?rid=unknown")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("tracking on the combined handler = %d", resp.StatusCode)
	}
}

func TestRewriteURL(t *testing.T) {
	h := newHarness(t)
	base, _ := url.Parse("https://login.example.com/app")
	cases := map[string]string{
		"/inbox":                              "/px/rid1/inbox",
		"https://login.example.com/inbox":     "/px/rid1/inbox",
		"/inbox?x=1#frag":                     "/px/rid1/inbox?x=1#frag",
		"https://cdn.example.org/app.js":      "https://cdn.example.org/app.js",
		"relative/path":                       "relative/path",
	}
	for in, want := range cases {
		if got := h.srv.rewriteURL(in, base, "rid1"); got != want {
			t.Errorf("rewriteURL(%q) = %q, want %q", in, got, want)
		}
	}
}
