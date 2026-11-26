package api

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOperatorCannotChangeSettings(t *testing.T) {
	h := newHarness(t)
	admin := h.login()
	h.expect(admin, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "op", "password": "operator-password-1", "role": "operator",
	}, http.StatusCreated)
	op := h.client()
	h.do(op, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "op", "password": "operator-password-1"})
	if status, _ := h.do(op, http.MethodPut, h.admin.URL+"/api/v1/settings",
		map[string]string{"org_name": "hijacked"}); status != http.StatusForbidden {
		t.Fatalf("operator changed settings: %d", status)
	}
	if status, _ := h.do(op, http.MethodGet, h.admin.URL+"/api/v1/settings", nil); status != http.StatusOK {
		t.Fatalf("operator cannot read settings: %d", status)
	}
}

func TestCampaignTargetSessionEndpoint(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.seedCampaign(t, c)
	h.expect(c, http.MethodPost, "/api/v1/campaigns/"+itoa(id)+"/launch", nil, http.StatusOK)
	h.waitCompleted(c, id)

	// No session captured yet.
	status, body := h.do(c, http.MethodGet, h.admin.URL+"/api/v1/campaigns/"+itoa(id)+"/targets/1/session", nil)
	if status != http.StatusOK {
		t.Fatalf("empty session lookup = %d %s", status, body)
	}

	// Seed one through the store, then read it back through the API.
	targets, err := h.st.CampaignTargets(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveProxySession(id, targets[0].RID, targets[0].Email, "login.example.com",
		map[string]string{"AUTH": "session-token"}); err != nil {
		t.Fatal(err)
	}
	got := h.expect(c, http.MethodGet,
		"/api/v1/campaigns/"+itoa(id)+"/targets/"+itoa(targets[0].ID)+"/session", nil, http.StatusOK)
	if got["upstream"] != "login.example.com" {
		t.Fatalf("upstream = %v", got["upstream"])
	}
	cookies := got["cookies"].(map[string]any)
	if cookies["AUTH"] != "session-token" {
		t.Fatalf("cookies = %v", cookies)
	}
	// Unknown target ids do not resolve.
	if status, _ := h.do(c, http.MethodGet,
		h.admin.URL+"/api/v1/campaigns/"+itoa(id)+"/targets/9999/session", nil); status != http.StatusNotFound {
		t.Fatalf("unknown target session = %d", status)
	}
}

func TestResendAfterFailure(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.seedCampaign(t, c)
	h.expect(c, http.MethodPost, "/api/v1/campaigns/"+itoa(id)+"/launch", nil, http.StatusOK)
	h.waitCompleted(c, id)

	targets, err := h.st.CampaignTargets(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkFailed(targets[0].ID, "connection refused"); err != nil {
		t.Fatal(err)
	}
	before := len(h.sender.sent)
	resend := h.expect(c, http.MethodPost, "/api/v1/campaigns/"+itoa(id)+"/resend", nil, http.StatusOK)
	if resend["status"] != "queued" {
		t.Fatalf("resend status = %v", resend["status"])
	}
	h.waitCompleted(c, id)
	after := len(h.sender.sent)
	// Only the failed recipient is resent, not the whole audience.
	if after != before+1 {
		t.Fatalf("resent %d messages, want 1", after-before)
	}
}

func TestTrustProxyUsesForwardedFor(t *testing.T) {
	h := newHarness(t)
	h.cfg.TrustProxy = true
	c := h.login()
	req, _ := http.NewRequest(http.MethodPost, h.admin.URL+"/api/v1/groups",
		strings.NewReader(`{"name":"proxy","targets":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	entries, err := h.st.AuditLog(1, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "group.saved" && e.IP == "203.0.113.7" {
			found = true
		}
	}
	if !found {
		t.Fatalf("forwarded IP not recorded: %+v", entries)
	}

	// With proxy trust off, the header is ignored.
	h.cfg.TrustProxy = false
	req2, _ := http.NewRequest(http.MethodPost, h.admin.URL+"/api/v1/groups",
		strings.NewReader(`{"name":"direct","targets":[]}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Forwarded-For", "198.51.100.9")
	resp2, err := c.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	entries, _ = h.st.AuditLog(1, 5)
	for _, e := range entries {
		if e.Action == "group.saved" && e.IP == "198.51.100.9" {
			t.Fatal("forwarded IP honoured while trust_proxy is off")
		}
	}
}

func TestMalformedRequests(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	cases := []struct {
		path string
		body string
	}{
		{"/api/v1/groups", `{"name":`},
		{"/api/v1/groups", `{"name":"x","unknown_field":1}`},
		{"/api/v1/templates", `[]`},
		{"/api/v1/campaigns", `{"name":123}`},
	}
	for _, tc := range cases {
		req, _ := http.NewRequest(http.MethodPost, h.admin.URL+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s with %s = %d, want 400", tc.path, tc.body, resp.StatusCode)
		}
	}
}

func TestDeleteScopedToOrg(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.seedCampaign(t, c)

	if _, err := h.st.DB().Exec(`INSERT INTO orgs (name, created_at) VALUES ('Other', 1)`); err != nil {
		t.Fatal(err)
	}
	h.expect(c, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "other2", "password": "another-long-password", "role": "admin",
	}, http.StatusCreated)
	if _, err := h.st.DB().Exec(`UPDATE users SET org_id = 2 WHERE username = 'other2'`); err != nil {
		t.Fatal(err)
	}
	other := h.client()
	h.do(other, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "other2", "password": "another-long-password"})

	// Deleting someone else's campaign reports a miss, not success.
	if status, _ := h.do(other, http.MethodDelete, h.admin.URL+"/api/v1/campaigns/"+itoa(id), nil); status != http.StatusNotFound {
		t.Fatalf("cross-tenant delete = %d, want 404", status)
	}
	if _, err := h.st.Campaign(1, id); err != nil {
		t.Fatalf("campaign disappeared: %v", err)
	}
	_ = time.Now
}

func TestProfileDelete(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	created := h.expect(c, http.MethodPost, "/api/v1/profiles", map[string]any{
		"name": "temp", "host": "smtp.example.com", "port": 587, "from_address": "it@example.com",
	}, http.StatusCreated)
	id := itoa(int64(created["id"].(float64)))
	h.expect(c, http.MethodDelete, "/api/v1/profiles/"+id, nil, http.StatusOK)
	h.expect(c, http.MethodGet, "/api/v1/profiles/"+id, nil, http.StatusNotFound)
	// A second delete reports the miss instead of pretending it worked.
	h.expect(c, http.MethodDelete, "/api/v1/profiles/"+id, nil, http.StatusNotFound)
}

func TestWebhookUpdate(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	created := h.expect(c, http.MethodPost, "/api/v1/webhooks", map[string]any{
		"name": "hook", "url": "https://siem.example.com/a", "enabled": true,
	}, http.StatusCreated)
	id := itoa(int64(created["id"].(float64)))
	updated := h.expect(c, http.MethodPut, "/api/v1/webhooks/"+id, map[string]any{
		"name": "hook renamed", "url": "https://siem.example.com/b",
		"events": []string{"email.opened", "link.clicked"}, "enabled": false,
	}, http.StatusOK)
	if updated["name"] != "hook renamed" || updated["enabled"] != false {
		t.Fatalf("update not applied: %v", updated)
	}
	events := updated["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("events = %v", events)
	}
	h.expect(c, http.MethodPut, "/api/v1/webhooks/999", map[string]any{
		"name": "x", "url": "https://x.example/h",
	}, http.StatusNotFound)
}

func TestTemplateAndPageUpdates(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	tpl := h.expect(c, http.MethodPost, "/api/v1/templates", map[string]any{
		"name": "t", "subject": "s", "html_body": "<p>x</p>",
	}, http.StatusCreated)
	tid := itoa(int64(tpl["id"].(float64)))
	h.expect(c, http.MethodPut, "/api/v1/templates/"+tid, map[string]any{
		"name": "", "subject": "s",
	}, http.StatusBadRequest)
	h.expect(c, http.MethodPut, "/api/v1/templates/999", map[string]any{
		"name": "x", "subject": "s",
	}, http.StatusNotFound)

	page := h.expect(c, http.MethodPost, "/api/v1/pages", map[string]any{
		"name": "p", "html": "<form></form>",
	}, http.StatusCreated)
	pid := itoa(int64(page["id"].(float64)))
	h.expect(c, http.MethodPut, "/api/v1/pages/"+pid, map[string]any{
		"name": "p2", "html": "<form><input name=q></form>", "capture": true,
	}, http.StatusOK)
	h.expect(c, http.MethodDelete, "/api/v1/pages/"+pid, nil, http.StatusOK)
	h.expect(c, http.MethodGet, "/api/v1/pages/"+pid, nil, http.StatusNotFound)
}

func TestAILandingPageDraftKind(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.srv.AI = fakeAIClient(t)
	draft := h.expect(c, http.MethodPost, "/api/v1/ai/generate", map[string]string{
		"kind": "landing_page", "prompt": "portal",
	}, http.StatusOK)
	if draft["html"] == "" || draft["name"] == "" {
		t.Fatalf("unexpected draft: %v", draft)
	}
}
