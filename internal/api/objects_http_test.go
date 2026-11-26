package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

func multipartBody(t *testing.T, field, filename string, content []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

func TestGroupCRUDAndValidation(t *testing.T) {
	h := newHarness(t)
	c := h.login()

	created := h.expect(c, http.MethodPost, "/api/v1/groups", map[string]any{
		"name": "Execs",
		"targets": []map[string]any{
			{"email": "alice@example.com", "first_name": "Alice", "custom": map[string]string{"team": "fin"}},
			{"email": "alice@example.com", "first_name": "Duplicate"},
			{"email": "bob@example.com"},
		},
	}, http.StatusCreated)
	if created["name"] != "Execs" {
		t.Fatalf("unexpected group: %v", created)
	}
	targets := created["targets"].([]any)
	if len(targets) != 2 {
		t.Fatalf("duplicate address kept: %d targets", len(targets))
	}
	id := int64(created["id"].(float64))

	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/groups",
		map[string]any{"name": "bad", "targets": []map[string]string{{"email": "not-an-email"}}}); status != http.StatusBadRequest {
		t.Fatalf("invalid recipient accepted: %d", status)
	}
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/groups",
		map[string]any{"name": "  ", "targets": []any{}}); status != http.StatusBadRequest {
		t.Fatalf("blank group name accepted: %d", status)
	}

	updated := h.expect(c, http.MethodPut, "/api/v1/groups/"+itoa(id), map[string]any{
		"name":    "Execs v2",
		"targets": []map[string]any{{"email": "carol@example.com"}},
	}, http.StatusOK)
	if len(updated["targets"].([]any)) != 1 {
		t.Fatalf("update did not replace targets: %v", updated["targets"])
	}

	list := h.expectRaw(c, http.MethodGet, "/api/v1/groups", nil, http.StatusOK)
	if !strings.Contains(string(list), "Execs v2") {
		t.Fatalf("list missing the group: %s", list)
	}

	h.expect(c, http.MethodDelete, "/api/v1/groups/"+itoa(id), nil, http.StatusOK)
	h.expect(c, http.MethodGet, "/api/v1/groups/"+itoa(id), nil, http.StatusNotFound)
}

func TestCSVImportEndpoint(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	body, contentType := multipartBody(t, "file", "recipients.csv",
		[]byte("Email,First Name,Last Name,Department\nalice@example.com,Alice,Ng,Finance\nbad-row\n"))
	req, _ := http.NewRequest(http.MethodPost, h.admin.URL+"/api/v1/groups/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CSV import = %d", resp.StatusCode)
	}
	var decoded struct {
		Count   int `json:"count"`
		Targets []struct {
			Email     string            `json:"email"`
			FirstName string            `json:"first_name"`
			Custom    map[string]string `json:"custom"`
		} `json:"targets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Count != 1 || decoded.Targets[0].FirstName != "Alice" {
		t.Fatalf("unexpected import result: %+v", decoded)
	}
	if decoded.Targets[0].Custom["department"] != "Finance" {
		t.Fatalf("custom column lost: %+v", decoded.Targets[0])
	}

	// A request without a file is rejected.
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/groups/import", map[string]string{}); status != http.StatusBadRequest {
		t.Fatalf("import without a file = %d, want 400", status)
	}
}

func TestTemplateCRUD(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	created := h.expect(c, http.MethodPost, "/api/v1/templates", map[string]any{
		"name": "T", "subject": "Hi {{.FirstName}}", "html_body": "<p>{{.Tracker}}</p>",
	}, http.StatusCreated)
	id := itoa(int64(created["id"].(float64)))
	if created["subject"] != "Hi {{.FirstName}}" {
		t.Fatalf("subject mangled: %v", created["subject"])
	}
	h.expect(c, http.MethodPut, "/api/v1/templates/"+id, map[string]any{
		"name": "T2", "subject": "s", "text_body": "b",
	}, http.StatusOK)
	got := h.expect(c, http.MethodGet, "/api/v1/templates/"+id, nil, http.StatusOK)
	if got["name"] != "T2" {
		t.Fatalf("update not saved: %v", got)
	}
	h.expect(c, http.MethodDelete, "/api/v1/templates/"+id, nil, http.StatusOK)
	h.expect(c, http.MethodGet, "/api/v1/templates/"+id, nil, http.StatusNotFound)
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/templates", map[string]any{"name": ""}); status != http.StatusBadRequest {
		t.Fatalf("nameless template accepted: %d", status)
	}
}

func TestPageFlagsRoundTrip(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	created := h.expect(c, http.MethodPost, "/api/v1/pages", map[string]any{
		"name": "P", "html": "<form></form>",
		"capture": true, "capture_values": true, "capture_passwords": false,
		"redirect_url": "https://example.com/x",
	}, http.StatusCreated)
	if created["capture_values"] != true || created["capture_passwords"] != false {
		t.Fatalf("capture flags not stored: %v", created)
	}
	id := itoa(int64(created["id"].(float64)))
	got := h.expect(c, http.MethodGet, "/api/v1/pages/"+id, nil, http.StatusOK)
	if got["redirect_url"] != "https://example.com/x" {
		t.Fatalf("redirect lost: %v", got)
	}
}

func TestImportLandingPage(t *testing.T) {
	h := newHarness(t)
	c := h.login()

	upstream := newUpstreamPage(t)
	got := h.expect(c, http.MethodPost, "/api/v1/pages/import-url", map[string]string{"url": upstream}, http.StatusOK)
	html := got["html"].(string)
	if !strings.Contains(html, "<form") {
		t.Fatalf("imported HTML lost the form: %s", html)
	}
	if !strings.Contains(html, "http://") || !strings.Contains(html, "/static/logo.png") {
		t.Fatalf("asset URLs were not absolutised: %s", html)
	}
	if strings.Contains(html, "integrity=") {
		t.Fatalf("integrity attributes survived: %s", html)
	}
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/pages/import-url",
		map[string]string{"url": "file:///etc/passwd"}); status != http.StatusBadRequest {
		t.Fatalf("non-http URL accepted: %d", status)
	}
}

func TestProfileSecrets(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	created := h.expect(c, http.MethodPost, "/api/v1/profiles", map[string]any{
		"name": "relay", "host": "smtp.example.com", "port": 587,
		"username": "u", "password": "smtp-secret",
		"from_address": "IT <it@example.com>",
	}, http.StatusCreated)
	if _, leaked := created["password"]; leaked {
		t.Fatal("SMTP password returned by the API")
	}
	if created["has_password"] != true {
		t.Fatal("has_password not reported")
	}
	id := int64(created["id"].(float64))

	// A name-only update keeps the stored password.
	h.expect(c, http.MethodPut, "/api/v1/profiles/"+itoa(id), map[string]any{
		"name": "relay renamed", "host": "smtp.example.com", "port": 587,
		"from_address": "IT <it@example.com>",
	}, http.StatusOK)
	stored, err := h.st.Profile(1, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Password != "smtp-secret" {
		t.Fatalf("password lost on update: %q", stored.Password)
	}
	// It is encrypted at rest.
	var raw string
	if err := h.st.DB().QueryRow(`SELECT password FROM profiles WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "smtp-secret") {
		t.Fatal("SMTP password stored in clear text")
	}

	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/profiles",
		map[string]any{"name": "bad", "host": "x", "from_address": "not-an-email"}); status != http.StatusBadRequest {
		t.Fatalf("invalid from address accepted: %d", status)
	}
}

func TestProfileTestSendRecordsResult(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.expect(c, http.MethodPost, "/api/v1/profiles", map[string]any{
		"name": "relay", "host": "127.0.0.1", "port": 2525, "from_address": "IT <it@example.com>",
	}, http.StatusCreated)

	h.expect(c, http.MethodPost, "/api/v1/profiles/1/test", map[string]string{"to": "me@example.com"}, http.StatusOK)
	if len(h.sender.sent) != 1 {
		t.Fatalf("test message not sent: %d", len(h.sender.sent))
	}
	profile := h.expect(c, http.MethodGet, "/api/v1/profiles/1", nil, http.StatusOK)
	if profile["last_test_at"] == nil {
		t.Fatal("successful test not recorded")
	}

	h.sender.err = errFake
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/profiles/1/test",
		map[string]string{"to": "me@example.com"}); status != http.StatusBadGateway {
		t.Fatalf("failed test send = %d, want 502", status)
	}
	profile = h.expect(c, http.MethodGet, "/api/v1/profiles/1", nil, http.StatusOK)
	if profile["last_test_error"] == "" {
		t.Fatal("failed test not recorded")
	}
}

func TestAttachments(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	body, contentType := multipartBody(t, "file", "invoice.pdf", []byte("%PDF-1.4 test"))
	req, _ := http.NewRequest(http.MethodPost, h.admin.URL+"/api/v1/attachments", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload = %d", resp.StatusCode)
	}
	var attachment struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&attachment); err != nil {
		t.Fatal(err)
	}
	if attachment.Name != "invoice.pdf" || attachment.Size == 0 {
		t.Fatalf("unexpected attachment: %+v", attachment)
	}

	status, raw := h.do(c, http.MethodGet, h.admin.URL+"/api/v1/attachments/"+itoa(attachment.ID)+"/download", nil)
	if status != http.StatusOK || string(raw) != "%PDF-1.4 test" {
		t.Fatalf("download = %d %q", status, raw)
	}

	list := h.expectRaw(c, http.MethodGet, "/api/v1/attachments", nil, http.StatusOK)
	if !strings.Contains(string(list), "invoice.pdf") {
		t.Fatalf("attachment not listed: %s", list)
	}
	h.expect(c, http.MethodDelete, "/api/v1/attachments/"+itoa(attachment.ID), nil, http.StatusOK)
}

func TestWebhookValidation(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	for _, bad := range []string{"", "ftp://x", "not a url"} {
		if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/webhooks",
			map[string]any{"name": "w", "url": bad}); status != http.StatusBadRequest {
			t.Errorf("webhook URL %q accepted (%d)", bad, status)
		}
	}
	created := h.expect(c, http.MethodPost, "/api/v1/webhooks", map[string]any{
		"name": "siem", "url": "https://siem.example.com/hook", "secret": "s", "enabled": true,
	}, http.StatusCreated)
	events := created["events"].([]any)
	if len(events) != 1 || events[0] != "*" {
		t.Fatalf("default event subscription wrong: %v", events)
	}
	if v, ok := created["secret"]; ok && v != "" {
		t.Fatal("webhook secret echoed back")
	}
	if created["has_secret"] != true {
		t.Fatal("has_secret not reported")
	}
	h.expect(c, http.MethodDelete, "/api/v1/webhooks/"+itoa(int64(created["id"].(float64))), nil, http.StatusOK)
}

func TestSettings(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	settings := h.expect(c, http.MethodGet, "/api/v1/settings", nil, http.StatusOK)
	if settings["phish_url"] != "https://phish.test" {
		t.Fatalf("phish URL = %v", settings["phish_url"])
	}
	if len(settings["events"].([]any)) == 0 {
		t.Fatal("event list missing")
	}
	h.expect(c, http.MethodPut, "/api/v1/settings", map[string]string{"org_name": "Acme"}, http.StatusOK)
	me := h.expect(c, http.MethodGet, "/api/v1/me", nil, http.StatusOK)
	if me["org_name"] != "Acme" {
		t.Fatalf("org rename not applied: %v", me["org_name"])
	}
}

func TestUnknownRoutesAndIDs(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.expect(c, http.MethodGet, "/api/v1/groups/999", nil, http.StatusNotFound)
	h.expect(c, http.MethodGet, "/api/v1/campaigns/999", nil, http.StatusNotFound)
	h.expect(c, http.MethodGet, "/api/v1/groups/not-a-number", nil, http.StatusBadRequest)
	if status, _ := h.do(c, http.MethodGet, h.admin.URL+"/api/v1/nope", nil); status != http.StatusNotFound {
		t.Fatalf("unknown endpoint = %d, want 404", status)
	}
}

func TestUIFallback(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	// Real asset.
	if status, raw := h.do(c, http.MethodGet, h.admin.URL+"/assets/app.js", nil); status != http.StatusOK || !strings.Contains(string(raw), "console.log") {
		t.Fatalf("asset = %d %q", status, raw)
	}
	// SPA route falls back to index.html.
	if status, raw := h.do(c, http.MethodGet, h.admin.URL+"/campaigns/1", nil); status != http.StatusOK || !strings.Contains(string(raw), "<!doctype html>") {
		t.Fatalf("SPA fallback = %d %q", status, raw)
	}
	// API paths never fall back.
	if status, _ := h.do(c, http.MethodGet, h.admin.URL+"/api/v1/missing", nil); status != http.StatusNotFound {
		t.Fatalf("API fallback leaked the UI: %d", status)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	resp, err := c.Get(h.admin.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "same-origin",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("no CSP on the admin UI")
	}
}
