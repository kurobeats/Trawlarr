package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// launchedCampaign seeds and launches a campaign, returning its id and the rid
// of the first recipient.
func launchedCampaign(t *testing.T, h *harness, c *http.Client) (int64, string) {
	t.Helper()
	id := h.seedCampaign(t, c)
	h.expect(c, http.MethodPost, "/api/v1/campaigns/"+itoa(id)+"/launch", nil, http.StatusOK)
	h.waitCompleted(c, id)
	detail := h.campaignDetail(c, id)
	results := detail["results"].([]any)
	first := results[0].(map[string]any)
	return id, first["rid"].(string)
}

func TestOpenPixel(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id, rid := launchedCampaign(t, h, c)

	status, body := h.do(nil, http.MethodGet, h.phish.URL+"/open.gif?rid="+rid, nil)
	if status != http.StatusOK || len(body) == 0 {
		t.Fatalf("pixel = %d, %d bytes", status, len(body))
	}
	// A second open must not create a second event.
	h.do(nil, http.MethodGet, h.phish.URL+"/open.gif?rid="+rid, nil)

	detail := h.campaignDetail(c, id)
	events := 0
	for _, raw := range detail["timeline"].([]any) {
		if raw.(map[string]any)["type"] == "email.opened" {
			events++
		}
	}
	if events != 1 {
		t.Fatalf("open events = %d, want 1", events)
	}
	if detail["stats"].(map[string]any)["opened"].(float64) != 1 {
		t.Fatalf("stats.opened = %v", detail["stats"])
	}
}

func TestClickRendersLandingPage(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	_, rid := launchedCampaign(t, h, c)

	status, body := h.do(nil, http.MethodGet, h.phish.URL+"/click?rid="+rid, nil)
	if status != http.StatusOK {
		t.Fatalf("click = %d", status)
	}
	html := string(body)
	if !strings.Contains(html, `action="/submit?rid=`+rid) {
		t.Fatalf("form not pointed at the capture endpoint:\n%s", html)
	}
	if !strings.Contains(html, `name="password"`) {
		t.Fatalf("landing page content missing:\n%s", html)
	}
}

func TestSubmitRecordsFieldsNotValues(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id, rid := launchedCampaign(t, h, c)

	status, body := h.postForm(h.noRedirect(), h.phish.URL+"/submit?rid="+rid,
		url.Values{"username": {"alice"}, "password": {"hunter2"}})
	if status != http.StatusSeeOther {
		t.Fatalf("submit = %d, want 303: %s", status, body)
	}

	detail := h.campaignDetail(c, id)
	for _, raw := range detail["results"].([]any) {
		row := raw.(map[string]any)
		if row["rid"] != rid {
			continue
		}
		fields, _ := row["fields"].([]any)
		if len(fields) != 2 {
			t.Fatalf("field names not recorded: %v", row["fields"])
		}
		if row["data"] != nil {
			t.Fatalf("values recorded although the page does not store them: %v", row["data"])
		}
		if row["status"] != "submitted" {
			t.Fatalf("status = %v", row["status"])
		}
	}
	if detail["stats"].(map[string]any)["submitted"].(float64) != 1 {
		t.Fatalf("submitted stat = %v", detail["stats"])
	}
}

func TestSubmitStoresValuesWhenEnabled(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	// Enable value capture on the page before launching.
	page := h.expect(c, http.MethodPost, "/api/v1/pages", map[string]any{
		"name": "capture-all", "html": `<form><input name="username"><input name="password" type="password"></form>`,
		"capture": true, "capture_values": true, "capture_passwords": true,
		"redirect_url": "https://example.com/thanks",
	}, http.StatusCreated)
	pageID := int(page["id"].(float64))
	id := h.seedCampaign(t, c)
	h.expect(c, http.MethodPut, "/api/v1/campaigns/"+itoa(id), map[string]any{
		"name": "Q4", "template_id": 1, "page_id": pageID, "profile_id": 1, "group_ids": []int{1},
	}, http.StatusOK)
	h.expect(c, http.MethodPost, "/api/v1/campaigns/"+itoa(id)+"/launch", nil, http.StatusOK)
	h.waitCompleted(c, id)
	rid := h.campaignDetail(c, id)["results"].([]any)[0].(map[string]any)["rid"].(string)

	if status, body := h.postForm(h.noRedirect(), h.phish.URL+"/submit?rid="+rid,
		url.Values{"username": {"alice"}, "password": {"hunter2"}}); status != http.StatusSeeOther {
		t.Fatalf("submit = %d: %s", status, body)
	}
	row := h.campaignDetail(c, id)["results"].([]any)[0].(map[string]any)
	data, ok := row["data"].(map[string]any)
	if !ok || data["username"] != "alice" || data["password"] != "hunter2" {
		t.Fatalf("values not captured: %v", row["data"])
	}
}

func TestReportAndOptOut(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id, rid := launchedCampaign(t, h, c)

	if status, body := h.do(nil, http.MethodGet, h.phish.URL+"/report?rid="+rid, nil); status != http.StatusOK {
		t.Fatalf("report = %d %s", status, body)
	}
	h.do(nil, http.MethodGet, h.phish.URL+"/report?rid="+rid, nil) // repeat is a no-op

	if status, _ := h.do(nil, http.MethodGet, h.phish.URL+"/optout?rid="+rid, nil); status != http.StatusOK {
		t.Fatalf("optout failed")
	}
	if !h.st.IsOptedOut("alice@example.com") && !h.st.IsOptedOut("bob@example.com") {
		t.Fatal("opt-out not recorded")
	}

	detail := h.campaignDetail(c, id)
	stats := detail["stats"].(map[string]any)
	if stats["reported"].(float64) != 1 {
		t.Fatalf("reported stat = %v", stats)
	}
	if stats["opted_out"].(float64) != 1 {
		t.Fatalf("opted_out stat = %v", stats)
	}
}

func TestTrackingRejectsUnknownRIDs(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{
		"/click?rid=doesnotexist1",
		"/open.gif?rid=doesnotexist1",
		"/report?rid=doesnotexist1",
		"/click",
		"/",
		"/nothing-here",
	} {
		status, body := h.do(nil, http.MethodGet, h.phish.URL+path, nil)
		if status != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, status)
		}
		lower := strings.ToLower(string(body))
		if strings.Contains(lower, "trawlarr") || strings.Contains(lower, "x-powered-by") {
			t.Errorf("%s leaks product identity: %s", path, body)
		}
	}
	// POST to an unknown rid is refused as well.
	if status, _ := h.postForm(h.noRedirect(), h.phish.URL+"/submit?rid=nope", url.Values{"a": {"b"}}); status != http.StatusNotFound {
		t.Fatalf("submit with unknown rid = %d", status)
	}
}

func TestRelayEndpointsDisabledByDefault(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	_, rid := launchedCampaign(t, h, c)
	for _, path := range []string{"/px/" + rid + "/login", "/px/" + rid + "/"} {
		if status, _ := h.do(nil, http.MethodGet, h.phish.URL+path, nil); status != http.StatusNotFound {
			t.Fatalf("%s = %d with relay disabled", path, status)
		}
	}
}

func TestPhishingListenerDoesNotExposeAdminAPI(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/api/v1/me", "/api/v1/campaigns", "/api/v1/login"} {
		if status, _ := h.do(nil, http.MethodGet, h.phish.URL+path, nil); status == http.StatusOK {
			t.Fatalf("%s served on the phishing listener", path)
		}
	}
}

func TestOptOutExcludesRecipientFromNextCampaign(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	_, rid := launchedCampaign(t, h, c)
	// Opt out the clicked recipient specifically.
	detail := h.campaignDetail(c, 1)
	var email string
	for _, raw := range detail["results"].([]any) {
		row := raw.(map[string]any)
		if row["rid"] == rid {
			email = row["email"].(string)
		}
	}
	h.do(nil, http.MethodGet, h.phish.URL+"/optout?rid="+rid, nil)

	h.expect(c, http.MethodPost, "/api/v1/campaigns", map[string]any{
		"name": "next", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
	}, http.StatusCreated)
	h.expect(c, http.MethodPost, "/api/v1/campaigns/2/launch", nil, http.StatusOK)
	h.waitCompleted(c, 2)

	second := h.campaignDetail(c, 2)
	for _, raw := range second["results"].([]any) {
		if raw.(map[string]any)["email"] == email {
			t.Fatalf("opted-out address was targeted again: %s", email)
		}
	}
}
