package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kurobeats/trawlarr/internal/ai"
	"github.com/kurobeats/trawlarr/internal/config"
)

// waitCompleted polls the API until the campaign finishes.
func (h *harness) waitCompleted(c *http.Client, id int64) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		detail := h.campaignDetail(c, id)
		switch detail["status"] {
		case "completed", "stopped", "error":
			return detail
		}
		time.Sleep(25 * time.Millisecond)
	}
	h.t.Fatalf("campaign %d did not finish", id)
	return nil
}

func TestCampaignLifecycleOverHTTP(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.seedCampaign(t, c)

	created := h.campaignDetail(c, id)
	if created["status"] != "draft" {
		t.Fatalf("new campaign status = %v", created["status"])
	}
	if created["stats"].(map[string]any)["total"].(float64) != 0 {
		t.Fatal("draft should have no recipients yet")
	}

	h.expect(c, http.MethodPost, "/api/v1/campaigns/"+itoa(id)+"/launch", nil, http.StatusOK)
	done := h.waitCompleted(c, id)
	stats := done["stats"].(map[string]any)
	if stats["sent"].(float64) != 2 || stats["total"].(float64) != 2 {
		t.Fatalf("unexpected stats: %v", stats)
	}
	if len(h.sender.sent) != 2 {
		t.Fatalf("engine sent %d messages", len(h.sender.sent))
	}
	if !strings.Contains(h.sender.sent[0].HTML, "/click?rid=") {
		t.Fatalf("tracked link missing from the email: %s", h.sender.sent[0].HTML)
	}

	// Unknown-to-most-people status transitions are rejected.
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/campaigns/"+itoa(id)+"/resend", nil); status != http.StatusBadRequest {
		t.Fatalf("resend without failures = %d, want 400", status)
	}
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/campaigns/"+itoa(id)+"/launch", nil); status != http.StatusConflict {
		t.Fatalf("relaunch of a finished campaign = %d", status)
	}
}

func TestCampaignValidation(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	for _, body := range []map[string]any{
		{"name": "", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1}},
		{"name": "x", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{}},
		{"name": "x", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1}, "send_rate": -5},
		{"name": "x", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1}, "proxy_url": "http://insecure.example"},
	} {
		if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/campaigns", body); status != http.StatusBadRequest {
			t.Errorf("invalid campaign accepted (%d): %v", status, body)
		}
	}

	// Proxy campaigns need the feature enabled and an allowlisted host.
	h.cfg.ProxyEnabled = true
	h.cfg.ProxyAllowlist = []string{"login.example.com"}
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/campaigns", map[string]any{
		"name": "relay", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
		"proxy_url": "https://login.example.com",
	}); status != http.StatusCreated {
		t.Fatalf("allowlisted relay campaign rejected: %d", status)
	}
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/campaigns", map[string]any{
		"name": "relay2", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
		"proxy_url": "https://not-allowed.example.net",
	}); status != http.StatusBadRequest {
		t.Fatalf("non-allowlisted relay campaign accepted: %d", status)
	}
}

func TestCampaignEditWhileRunningIsRejected(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.seedCampaign(t, c)
	if err := h.st.SetCampaignStatus(id, "sending"); err != nil {
		t.Fatal(err)
	}
	status, _ := h.do(c, http.MethodPut, h.admin.URL+"/api/v1/campaigns/"+itoa(id), map[string]any{
		"name": "renamed", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
	})
	if status != http.StatusConflict {
		t.Fatalf("editing a sending campaign = %d, want 409", status)
	}
}

func TestCampaignExportAndDelete(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.seedCampaign(t, c)
	h.expect(c, http.MethodPost, "/api/v1/campaigns/"+itoa(id)+"/launch", nil, http.StatusOK)
	h.waitCompleted(c, id)

	status, csv := h.do(c, http.MethodGet, h.admin.URL+"/api/v1/campaigns/"+itoa(id)+"/export", nil)
	if status != http.StatusOK {
		t.Fatalf("export = %d", status)
	}
	if !strings.HasPrefix(string(csv), "email,first_name") || !strings.Contains(string(csv), "alice@example.com") {
		t.Fatalf("unexpected CSV:\n%s", csv)
	}
	h.expect(c, http.MethodDelete, "/api/v1/campaigns/"+itoa(id), nil, http.StatusOK)
	h.expect(c, http.MethodGet, "/api/v1/campaigns/"+itoa(id), nil, http.StatusNotFound)
}

func TestCampaignStoppedFromAnotherProcess(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.seedCampaign(t, c)

	// A slow send gives us time to stop it through the API.
	h.expect(c, http.MethodPut, "/api/v1/campaigns/"+itoa(id), map[string]any{
		"name": "Q4 slow", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
		"send_rate": 3600,
	}, http.StatusOK)
	h.expect(c, http.MethodPost, "/api/v1/campaigns/"+itoa(id)+"/launch", nil, http.StatusOK)
	time.Sleep(150 * time.Millisecond)
	h.expect(c, http.MethodPost, "/api/v1/campaigns/"+itoa(id)+"/stop", nil, http.StatusOK)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if status, _ := h.st.CampaignStatus(id); status == "stopped" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	status, _ := h.st.CampaignStatus(id)
	t.Fatalf("campaign did not stop, status = %q", status)
}

func TestDashboardAggregates(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.seedCampaign(t, c)
	h.expect(c, http.MethodPost, "/api/v1/campaigns/"+itoa(id)+"/launch", nil, http.StatusOK)
	h.waitCompleted(c, id)

	dash := h.expect(c, http.MethodGet, "/api/v1/dashboard", nil, http.StatusOK)
	if dash["campaign_count"].(float64) != 1 {
		t.Fatalf("campaign_count = %v", dash["campaign_count"])
	}
	stats := dash["stats"].(map[string]any)
	if stats["sent"].(float64) != 2 {
		t.Fatalf("dashboard sent = %v", stats["sent"])
	}
	counts := dash["object_counts"].(map[string]any)
	if counts["groups"].(float64) != 1 || counts["templates"].(float64) != 1 {
		t.Fatalf("object counts = %v", counts)
	}
}

func TestCampaignIsolationBetweenOrgs(t *testing.T) {
	h := newHarness(t)
	admin := h.login()
	id := h.seedCampaign(t, admin)

	// A second tenant with its own admin.
	if _, err := h.st.DB().Exec(`INSERT INTO orgs (name, created_at) VALUES ('Other', 1)`); err != nil {
		t.Fatal(err)
	}
	h.expect(admin, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "other-admin", "password": "another-long-password", "role": "admin",
	}, http.StatusCreated)
	if _, err := h.st.DB().Exec(`UPDATE users SET org_id = 2 WHERE username = 'other-admin'`); err != nil {
		t.Fatal(err)
	}

	login := h.client()
	if status, body := h.do(login, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "other-admin", "password": "another-long-password"}); status != http.StatusOK {
		t.Fatalf("second tenant login failed: %d %s", status, body)
	}
	if status, _ := h.do(login, http.MethodGet, h.admin.URL+"/api/v1/campaigns/"+itoa(id), nil); status != http.StatusNotFound {
		t.Fatalf("cross-tenant campaign read = %d, want 404", status)
	}
	list := h.expectRaw(login, http.MethodGet, "/api/v1/campaigns", nil, http.StatusOK)
	if strings.Contains(string(list), "Q4") {
		t.Fatalf("other tenant saw campaigns: %s", list)
	}
}

func TestAIEndpoints(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.seedCampaign(t, c)

	status := h.expect(c, http.MethodGet, "/api/v1/ai/status", nil, http.StatusOK)
	if status["enabled"] != false {
		t.Fatalf("AI should be disabled by default: %v", status)
	}
	if code, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/ai/generate",
		map[string]string{"kind": "email", "prompt": "x"}); code != http.StatusServiceUnavailable {
		t.Fatalf("generate while disabled = %d", code)
	}
	if code, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/campaigns/"+itoa(id)+"/analyze", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("analyze while disabled = %d", code)
	}

	var sawRecipients bool
	var bodies []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		for _, m := range req.Messages {
			bodies = append(bodies, m.Content)
			if strings.Contains(m.Content, "@example.com") {
				sawRecipients = true
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": `{"subject":"S","text_body":"t","html_body":"<p>t</p>","name":"P","html":"<form></form>"}`,
			}}},
		})
	}))
	defer upstream.Close()
	h.srv.AI = ai.New(config.AI{Enabled: true, BaseURL: upstream.URL, Model: "m"})

	status = h.expect(c, http.MethodGet, "/api/v1/ai/status", nil, http.StatusOK)
	if status["enabled"] != true || status["model"] != "m" {
		t.Fatalf("AI status = %v", status)
	}

	draft := h.expect(c, http.MethodPost, "/api/v1/ai/generate",
		map[string]string{"kind": "email", "prompt": "payroll reminder"}, http.StatusOK)
	if draft["subject"] != "S" {
		t.Fatalf("unexpected draft: %v", draft)
	}
	if _, ok := draft["text_body"]; !ok {
		t.Fatalf("draft missing text body: %v", draft)
	}

	analysis := h.expect(c, http.MethodPost, "/api/v1/campaigns/"+itoa(id)+"/analyze", nil, http.StatusOK)
	if analysis["analysis"] == "" {
		t.Fatalf("empty analysis: %v", analysis)
	}
	if sawRecipients {
		t.Fatal("recipient addresses were sent to the AI endpoint")
	}
	if len(bodies) == 0 {
		t.Fatal("no prompt reached the AI endpoint")
	}

	// Bad input is rejected before reaching the model.
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/ai/generate",
		map[string]string{"kind": "email", "prompt": "  "}); status != http.StatusBadRequest {
		t.Fatalf("empty prompt = %d", status)
	}
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/ai/generate",
		map[string]string{"kind": "everything", "prompt": "x"}); status != http.StatusBadRequest {
		t.Fatalf("unknown kind = %d", status)
	}
}
