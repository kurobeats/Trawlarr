package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestPreviewTemplate(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	created := h.expect(c, http.MethodPost, "/api/v1/templates", map[string]any{
		"name":      "Invoice",
		"subject":   "Invoice for {{.FirstName}} {{.LastName}}",
		"text_body": "Hi {{.FirstName}}, pay at {{.URL}}",
		"html_body": `<html><body><h1>Hi {{.FirstName}}</h1>` +
			`<a href="{{.URL}}">Pay</a>{{.Tracker}}` +
			`<a href="{{.OptOutURL}}">unsubscribe</a></body></html>`,
	}, http.StatusCreated)
	id := itoa(int64(created["id"].(float64)))

	got := h.expect(c, http.MethodGet, "/api/v1/templates/"+id+"/preview", nil, http.StatusOK)
	if got["subject"] != "Invoice for Alex Morgan" {
		t.Fatalf("subject = %v", got["subject"])
	}
	text := got["text_body"].(string)
	if !strings.Contains(text, "Hi Alex") {
		t.Fatalf("text body not rendered: %q", text)
	}
	// The plain-text body keeps the tracked URL so the writer can see it.
	if !strings.Contains(text, "/click?rid=preview") {
		t.Fatalf("text body lost the URL: %q", text)
	}

	html := got["html_body"].(string)
	if !strings.Contains(html, "<h1>Hi Alex</h1>") {
		t.Fatalf("html not rendered: %s", html)
	}
	// Preview markup must not reach live tracking endpoints.
	if strings.Contains(html, "/click?rid=preview") || strings.Contains(html, "/open.gif") ||
		strings.Contains(html, "/optout") {
		t.Fatalf("preview kept live tracking links: %s", html)
	}
	if !strings.Contains(html, `href="#"`) {
		t.Fatalf("links were not made inert: %s", html)
	}
	if !strings.Contains(html, "data:image/gif;base64,") {
		t.Fatalf("tracker was not replaced with an inline pixel: %s", html)
	}
	if attachments, ok := got["attachments"].([]any); !ok || len(attachments) != 0 {
		t.Fatalf("attachments = %v", got["attachments"])
	}
}

func TestPreviewTemplateTextOnly(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	created := h.expect(c, http.MethodPost, "/api/v1/templates", map[string]any{
		"name": "Plain", "subject": "Hi", "text_body": "Just text for {{.FirstName}}",
	}, http.StatusCreated)
	got := h.expect(c, http.MethodGet, "/api/v1/templates/"+itoa(int64(created["id"].(float64)))+"/preview", nil, http.StatusOK)
	if got["html_body"] != "" {
		t.Fatalf("html_body = %q", got["html_body"])
	}
	if !strings.Contains(got["text_body"].(string), "Just text for Alex") {
		t.Fatalf("text body = %q", got["text_body"])
	}
}

func TestPreviewTemplateErrors(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.expect(c, http.MethodGet, "/api/v1/templates/999/preview", nil, http.StatusNotFound)
	if status, _ := h.do(h.client(), http.MethodGet, h.admin.URL+"/api/v1/templates/1/preview", nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated preview = %d", status)
	}

	bad := h.expect(c, http.MethodPost, "/api/v1/templates", map[string]any{
		"name": "Broken", "subject": "ok", "html_body": "{{.Nope}}",
	}, http.StatusCreated)
	status, body := h.do(c, http.MethodGet, h.admin.URL+"/api/v1/templates/"+itoa(int64(bad["id"].(float64)))+"/preview", nil)
	if status != http.StatusBadRequest {
		t.Fatalf("broken template preview = %d: %s", status, body)
	}
	if !strings.Contains(string(body), "html body") {
		t.Fatalf("error does not name the broken field: %s", body)
	}
}

func TestPreviewPageNeutralisesLinksToo(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	created := h.expect(c, http.MethodPost, "/api/v1/pages", map[string]any{
		"name": "Portal",
		"html": `<a href="{{.URL}}">go</a><form action="/submit?rid={{.RId}}"></form>`,
	}, http.StatusCreated)
	status, body := h.do(c, http.MethodGet,
		h.admin.URL+"/api/v1/pages/"+itoa(int64(created["id"].(float64)))+"/preview", nil)
	if status != http.StatusOK {
		t.Fatalf("preview = %d", status)
	}
	if strings.Contains(string(body), "/click?rid=preview") {
		t.Fatalf("page preview kept the tracking link: %s", body)
	}
}
