package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kurobeats/trawlarr/internal/config"
	"github.com/kurobeats/trawlarr/internal/imapmon"
)

func TestSettingsExposeEveryEditableValue(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	got := h.expect(c, http.MethodGet, "/api/v1/settings", nil, http.StatusOK)

	// Application
	if got["org_name"] != "Default" {
		t.Fatalf("org_name = %v", got["org_name"])
	}
	if got["phish_url"] != "https://phish.test" {
		t.Fatalf("phish_url = %v", got["phish_url"])
	}
	// Reporting
	imapSection, ok := got["imap"].(map[string]any)
	if !ok {
		t.Fatalf("imap section missing: %v", got)
	}
	for _, key := range []string{"enabled", "host", "port", "username", "use_tls", "folder", "restrict_domain", "delete_reported", "interval_minutes", "has_password"} {
		if _, present := imapSection[key]; !present {
			t.Errorf("imap.%s missing from settings payload", key)
		}
	}
	// AI
	aiSection, ok := got["ai"].(map[string]any)
	if !ok {
		t.Fatalf("ai section missing: %v", got)
	}
	for _, key := range []string{"enabled", "base_url", "model", "timeout_seconds", "has_api_key"} {
		if _, present := aiSection[key]; !present {
			t.Errorf("ai.%s missing from settings payload", key)
		}
	}
	if _, leaked := imapSection["password"]; leaked {
		t.Error("IMAP password returned")
	}
	if _, leaked := aiSection["api_key"]; leaked {
		t.Error("AI key returned")
	}
}

func TestSettingsUpdateIMAP(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{
		"imap": map[string]any{
			"enabled": true, "host": "imap.example.com", "port": 1993, "username": "soc",
			"password": "mail-secret", "use_tls": true, "folder": "Reports",
			"restrict_domain": "corp.example", "delete_reported": true, "interval_minutes": 2,
		},
	}, http.StatusOK)

	got := h.expect(c, http.MethodGet, "/api/v1/settings", nil, http.StatusOK)
	imapSection := got["imap"].(map[string]any)
	if imapSection["host"] != "imap.example.com" || imapSection["port"].(float64) != 1993 {
		t.Fatalf("imap not saved: %v", imapSection)
	}
	if imapSection["has_password"] != true {
		t.Fatal("password not stored")
	}
	if _, leaked := imapSection["password"]; leaked {
		t.Fatal("password returned in plain text")
	}

	// The monitor reads the same values.
	loaded := imapmon.Load(h.st, h.cfg)
	if loaded.Host != "imap.example.com" || loaded.Port != 1993 || loaded.Folder != "Reports" ||
		loaded.Password != "mail-secret" || loaded.RestrictDomain != "corp.example" || !loaded.Delete {
		t.Fatalf("monitor settings = %+v", loaded)
	}
	if !loaded.Enabled() {
		t.Fatal("monitor not enabled by settings")
	}

	// Stored encrypted at rest.
	var raw string
	if err := h.st.DB().QueryRow(`SELECT value FROM settings WHERE key = 'imap_password'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "mail-secret") {
		t.Fatal("IMAP password stored in clear text")
	}

	// The password survives a partial update, and can be cleared explicitly.
	h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{
		"imap": map[string]any{"enabled": true, "host": "imap.example.com", "username": "soc", "port": 1993},
	}, http.StatusOK)
	if imapmon.Load(h.st, h.cfg).Password != "mail-secret" {
		t.Fatal("password lost on partial update")
	}
	h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{
		"imap": map[string]any{"enabled": false, "clear_password": true},
	}, http.StatusOK)
	if imapmon.Load(h.st, h.cfg).Password != "" {
		t.Fatal("password not cleared")
	}
}

func TestSettingsUpdateAI(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	upstream := httptest.NewServer(fakeAIHandler())
	defer upstream.Close()

	h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{
		"ai": map[string]any{
			"enabled": true, "base_url": upstream.URL, "model": "stub-model",
			"api_key": "sk-secret", "timeout_seconds": 30,
		},
	}, http.StatusOK)

	// The client is rebuilt immediately, so the status endpoint reflects it.
	status := h.expect(c, http.MethodGet, "/api/v1/ai/status", nil, http.StatusOK)
	if status["enabled"] != true || status["model"] != "stub-model" {
		t.Fatalf("AI status = %v", status)
	}
	if h.srv.AI == nil {
		t.Fatal("AI client not rebuilt")
	}
	// Drafting works with the stored endpoint.
	if draft := h.expect(c, http.MethodPost, "/api/v1/ai/generate",
		map[string]string{"kind": "email", "prompt": "hello"}, http.StatusOK); draft["subject"] == "" {
		t.Fatalf("draft failed: %v", draft)
	}
	// The key is stored encrypted and never returned.
	var raw string
	if err := h.st.DB().QueryRow(`SELECT value FROM settings WHERE key = 'ai_api_key'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "sk-secret") {
		t.Fatal("AI key stored in clear text")
	}
	got := h.expect(c, http.MethodGet, "/api/v1/settings", nil, http.StatusOK)
	if got["ai"].(map[string]any)["has_api_key"] != true {
		t.Fatal("has_api_key not reported")
	}

	// Disabling removes the client.
	h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{
		"ai": map[string]any{"enabled": false},
	}, http.StatusOK)
	status = h.expect(c, http.MethodGet, "/api/v1/ai/status", nil, http.StatusOK)
	if status["enabled"] != false {
		t.Fatalf("AI still enabled: %v", status)
	}
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/ai/generate",
		map[string]string{"kind": "email", "prompt": "x"}); status != http.StatusServiceUnavailable {
		t.Fatalf("generate after disabling = %d", status)
	}
}

func TestSettingsValidation(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	cases := []map[string]any{
		{"org_name": "   "},
		{"phish_public_url": "not-a-url"},
		{"phish_public_url": "ftp://example.com"},
		{"imap": map[string]any{"enabled": true, "host": ""}},
		{"imap": map[string]any{"enabled": true, "host": "imap.example.com", "username": ""}},
		{"imap": map[string]any{"port": 70000}},
		{"imap": map[string]any{"interval_minutes": 5000}},
		{"ai": map[string]any{"enabled": true, "base_url": ""}},
		{"ai": map[string]any{"enabled": true, "base_url": "http://x", "model": ""}},
		{"ai": map[string]any{"base_url": "file:///etc/passwd"}},
		{"ai": map[string]any{"timeout_seconds": 2}},
	}
	for _, body := range cases {
		if status, resp := h.do(c, http.MethodPut, h.admin.URL+"/api/v1/settings", body); status != http.StatusBadRequest {
			t.Errorf("settings %v accepted (%d): %s", body, status, resp)
		}
	}
}

func TestSettingsPermissions(t *testing.T) {
	h := newHarness(t)
	admin := h.login()
	h.expect(admin, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "op", "password": "operator-password-1", "role": "operator",
	}, http.StatusCreated)
	op := h.client()
	h.do(op, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "op", "password": "operator-password-1"})

	if status, _ := h.do(op, http.MethodGet, h.admin.URL+"/api/v1/settings", nil); status != http.StatusOK {
		t.Fatalf("operator cannot read settings: %d", status)
	}
	for _, body := range []map[string]any{
		{"org_name": "Renamed"},
		{"imap": map[string]any{"host": "x"}},
		{"ai": map[string]any{"enabled": false}},
	} {
		if status, _ := h.do(op, http.MethodPut, h.admin.URL+"/api/v1/settings", body); status != http.StatusForbidden {
			t.Errorf("operator changed settings with %v: %d", body, status)
		}
	}
}

func TestTrackingBaseURLOverride(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{
		"phish_public_url": "https://training.example.com/",
	}, http.StatusOK)

	got := h.expect(c, http.MethodGet, "/api/v1/settings", nil, http.StatusOK)
	if got["phish_url"] != "https://training.example.com" {
		t.Fatalf("tracking base = %v", got["phish_url"])
	}
	// /me must agree with /settings, since the UI shows it in the sidebar.
	me := h.expect(c, http.MethodGet, "/api/v1/me", nil, http.StatusOK)
	if me["phish_url"] != "https://training.example.com" {
		t.Fatalf("me.phish_url = %v", me["phish_url"])
	}

	// New emails use the override when the campaign does not set its own URL.
	id := h.seedCampaign(t, c)
	h.expect(c, http.MethodPut, "/api/v1/campaigns/"+itoa(id), map[string]any{
		"name": "Q4", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1}, "url": "",
	}, http.StatusOK)
	h.expect(c, http.MethodPost, "/api/v1/campaigns/"+itoa(id)+"/launch", nil, http.StatusOK)
	h.waitCompleted(c, id)
	if len(h.sender.sent) == 0 {
		t.Fatal("nothing was sent")
	}
	if !strings.Contains(h.sender.sent[0].HTML, "https://training.example.com/click?rid=") {
		t.Fatalf("override not used in the email: %s", h.sender.sent[0].HTML)
	}
}

func TestSettingsAuditTrail(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{"org_name": "Acme"}, http.StatusOK)
	raw := h.expectRaw(c, http.MethodGet, "/api/v1/audit", nil, http.StatusOK)
	if !strings.Contains(string(raw), "settings.updated") {
		t.Fatalf("settings change not audited: %s", raw)
	}
}

// fakeAIHandler answers every request with a usable draft.
func fakeAIHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"subject\":\"S\",\"text_body\":\"t\",\"html_body\":\"<p>t</p>\",\"name\":\"P\",\"html\":\"<form></form>\"}"}}]}`))
	})
}

func TestMCPSettings(t *testing.T) {
	h := newHarness(t)
	c := h.login()

	got := h.expect(c, http.MethodGet, "/api/v1/settings", nil, http.StatusOK)
	mcpSection, ok := got["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp section missing: %v", got)
	}
	for _, key := range []string{"enabled", "allow_writes", "user"} {
		if _, present := mcpSection[key]; !present {
			t.Errorf("mcp.%s missing from settings payload", key)
		}
	}

	h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{
		"mcp": map[string]any{"enabled": true, "allow_writes": true, "user": "admin"},
	}, http.StatusOK)
	got = h.expect(c, http.MethodGet, "/api/v1/settings", nil, http.StatusOK)
	mcpSection = got["mcp"].(map[string]any)
	if mcpSection["enabled"] != true || mcpSection["allow_writes"] != true || mcpSection["user"] != "admin" {
		t.Fatalf("mcp settings not saved: %v", mcpSection)
	}

	// The MCP process reads the same keys through the config resolver.
	eff := h.cfg.MCPFrom(h.st)
	if !eff.Enabled || !eff.AllowWrites || eff.User != "admin" {
		t.Fatalf("resolver disagrees with the API: %+v", eff)
	}

	// Unknown or disabled accounts are refused.
	if status, _ := h.do(c, http.MethodPut, h.admin.URL+"/api/v1/settings",
		map[string]any{"mcp": map[string]any{"user": "ghost"}}); status != http.StatusBadRequest {
		t.Fatalf("unknown MCP user = %d", status)
	}
	h.expect(c, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "leaver", "password": "another-long-password", "role": "operator",
	}, http.StatusCreated)
	leaver, _ := h.st.UserByUsername("leaver")
	leaver.Disabled = true
	if err := h.st.UpdateUser(leaver); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.do(c, http.MethodPut, h.admin.URL+"/api/v1/settings",
		map[string]any{"mcp": map[string]any{"user": "leaver"}}); status != http.StatusBadRequest {
		t.Fatalf("disabled MCP user = %d", status)
	}

	// Clearing the user falls back to the first admin.
	h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{
		"mcp": map[string]any{"enabled": true, "user": ""},
	}, http.StatusOK)
	if h.cfg.MCPFrom(h.st).User != "" {
		t.Fatal("clearing the MCP user did not stick")
	}
}

func TestAdminURLSetting(t *testing.T) {
	h := newHarness(t)
	c := h.login()

	got := h.expect(c, http.MethodGet, "/api/v1/settings", nil, http.StatusOK)
	if got["admin_url"] != "http://127.0.0.1:0" && got["admin_url"] == "" {
		t.Fatalf("admin_url missing: %v", got["admin_url"])
	}

	res := h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{
		"admin_public_url": "https://console.example.com/",
	}, http.StatusOK)
	if res["restart_required"] != true {
		t.Fatalf("admin URL should advise a restart: %v", res)
	}
	reasons := res["restart_reasons"].([]any)
	if len(reasons) != 1 || reasons[0] != "admin URL" {
		t.Fatalf("reasons = %v", reasons)
	}

	got = h.expect(c, http.MethodGet, "/api/v1/settings", nil, http.StatusOK)
	if got["admin_url"] != "https://console.example.com" {
		t.Fatalf("admin_url = %v", got["admin_url"])
	}

	// The configured admin origin is accepted for state-changing requests even
	// when the browser sends a different Host.
	req, _ := http.NewRequest(http.MethodPost, h.admin.URL+"/api/v1/groups",
		strings.NewReader(`{"name":"cross","targets":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://console.example.com")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin origin rejected: %d", resp.StatusCode)
	}

	// Validation and the no-restart case.
	if status, _ := h.do(c, http.MethodPut, h.admin.URL+"/api/v1/settings",
		map[string]any{"admin_public_url": "ftp://nope"}); status != http.StatusBadRequest {
		t.Fatalf("bad admin URL = %d", status)
	}
	res = h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{"org_name": "Acme"}, http.StatusOK)
	if res["restart_required"] != false {
		t.Fatalf("org rename asked for a restart: %v", res)
	}
}

func TestApplicationSettingsPayload(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	got := h.expect(c, http.MethodGet, "/api/v1/settings", nil, http.StatusOK)
	app, ok := got["application"].(map[string]any)
	if !ok {
		t.Fatalf("application section missing: %v", got)
	}
	for _, key := range []string{"admin_listen", "phish_listen", "db_path", "proxy_enabled", "proxy_allowlist"} {
		if _, present := app[key]; !present {
			t.Errorf("application.%s missing", key)
		}
	}
	if app["db_path"] != h.cfg.DBPath {
		t.Fatalf("db_path = %v, want %v", app["db_path"], h.cfg.DBPath)
	}
}

func TestApplicationSettingsSave(t *testing.T) {
	h := newHarness(t)
	c := h.login()

	res := h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{
		"application": map[string]any{
			"admin_listen":    "127.0.0.1:9000",
			"phish_listen":    "http://0.0.0.0:9090/",
			"db_path":         "/tmp/trawlarr-alt.db",
			"proxy_enabled":   true,
			"proxy_allowlist": []string{"Login.Example.com", "*.corp.internal", "login.example.com"},
		},
	}, http.StatusOK)
	if res["restart_required"] != true {
		t.Fatalf("listener changes should need a restart: %v", res)
	}
	reasons := res["restart_reasons"].([]any)
	if len(reasons) == 0 || reasons[0] != "listeners and database path" {
		t.Fatalf("reasons = %v", reasons)
	}

	got := h.expect(c, http.MethodGet, "/api/v1/settings", nil, http.StatusOK)
	app := got["application"].(map[string]any)
	if app["admin_listen"] != "127.0.0.1:9000" {
		t.Fatalf("admin listen = %v", app["admin_listen"])
	}
	if app["phish_listen"] != "0.0.0.0:9090" {
		t.Fatalf("phish listen = %v", app["phish_listen"])
	}
	if app["db_path"] != "/tmp/trawlarr-alt.db" {
		t.Fatalf("db path = %v", app["db_path"])
	}
	allow := app["proxy_allowlist"].([]any)
	if len(allow) != 2 || allow[0] != "login.example.com" || allow[1] != "*.corp.internal" {
		t.Fatalf("allowlist = %v", allow)
	}

	// The relay policy applies without a restart.
	enabled, list := h.cfg.RelayPolicy()
	if !enabled || len(list) != 2 {
		t.Fatalf("relay policy = %v %v", enabled, list)
	}
	if !h.cfg.ProxyHostAllowed("login.example.com") || !h.cfg.ProxyHostAllowed("a.corp.internal") {
		t.Fatal("allowlisted host refused")
	}
}

func TestApplicationSettingsValidation(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	cases := []map[string]any{
		{"application": map[string]any{"admin_listen": ""}},
		{"application": map[string]any{"admin_listen": "nonsense"}},
		{"application": map[string]any{"admin_listen": "127.0.0.1"}},
		{"application": map[string]any{"admin_listen": "ftp://127.0.0.1:21"}},
		{"application": map[string]any{"phish_listen": "127.0.0.1:99999"}},
		{"application": map[string]any{"phish_listen": "http://host:80/path"}},
		{"application": map[string]any{"db_path": ""}},
		{"application": map[string]any{"db_path": t.TempDir()}},
		{"application": map[string]any{"proxy_allowlist": []string{"https://example.com"}}},
		{"application": map[string]any{"proxy_allowlist": []string{"example.com:443"}}},
	}
	for _, body := range cases {
		if status, resp := h.do(c, http.MethodPut, h.admin.URL+"/api/v1/settings", body); status != http.StatusBadRequest {
			t.Errorf("%v accepted (%d): %s", body, status, resp)
		}
	}

	// Relay-only changes apply without asking for a restart.
	res := h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{
		"application": map[string]any{"proxy_enabled": false},
	}, http.StatusOK)
	if res["restart_required"] != false {
		t.Fatalf("relay change asked for a restart: %v", res)
	}
}

func TestApplicationSettingsPersistToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessWithConfig(t, loaded)
	c := h.login()
	h.expect(c, http.MethodPut, "/api/v1/settings", map[string]any{
		"application": map[string]any{"admin_listen": "127.0.0.1:9100", "db_path": filepath.Join(dir, "alt.db")},
	}, http.StatusOK)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk["db_path"] != filepath.Join(dir, "alt.db") {
		t.Fatalf("db_path not written to config.json: %v", onDisk["db_path"])
	}
	admin := onDisk["admin_server"].(map[string]any)
	if admin["listen_url"] != "127.0.0.1:9100" {
		t.Fatalf("listener not written: %v", admin["listen_url"])
	}
}
