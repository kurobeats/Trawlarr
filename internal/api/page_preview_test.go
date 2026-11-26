package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestPreviewPage(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	created := h.expect(c, http.MethodPost, "/api/v1/pages", map[string]any{
		"name": "Portal",
		"html": `<html><head><title>Sign in</title></head><body>` +
			`<h1>Hello {{.FirstName}}</h1>` +
			`<form action="https://collector.invalid"><input name="username"></form>` +
			`<script>alert(1)</script></body></html>`,
	}, http.StatusCreated)
	id := itoa(int64(created["id"].(float64)))

	status, body := h.do(c, http.MethodGet, h.admin.URL+"/api/v1/pages/"+id+"/preview", nil)
	if status != http.StatusOK {
		t.Fatalf("preview = %d: %s", status, body)
	}
	html := string(body)
	if !strings.Contains(html, "Hello Alex") {
		t.Fatalf("placeholders not rendered: %s", html)
	}
	if !strings.Contains(html, `<input name="username">`) {
		t.Fatalf("page markup missing: %s", html)
	}

	// The preview is served on the admin origin, so it must be inert.
	resp, err := c.Get(h.admin.URL + "/api/v1/pages/" + id + "/preview")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"sandbox", "default-src 'none'", "frame-ancestors 'self'", "form-action 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("preview CSP is missing %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "script-src") && !strings.Contains(csp, "script-src 'none'") {
		t.Errorf("preview CSP permits scripts: %s", csp)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "" {
		t.Errorf("X-Frame-Options blocks the preview iframe: %q", got)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("preview is cacheable: %q", resp.Header.Get("Cache-Control"))
	}
}

func TestPreviewPageErrors(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.expect(c, http.MethodGet, "/api/v1/pages/999/preview", nil, http.StatusNotFound)
	if status, _ := h.do(h.client(), http.MethodGet, h.admin.URL+"/api/v1/pages/1/preview", nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated preview = %d", status)
	}

	// A broken template reports the error inside the frame instead of blanking it.
	bad := h.expect(c, http.MethodPost, "/api/v1/pages", map[string]any{
		"name": "Broken", "html": "{{.FirstName",
	}, http.StatusCreated)
	status, body := h.do(c, http.MethodGet, h.admin.URL+"/api/v1/pages/"+itoa(int64(bad["id"].(float64)))+"/preview", nil)
	if status != http.StatusOK || !strings.Contains(string(body), "Template error") {
		t.Fatalf("template error not reported: %d %s", status, body)
	}
	if strings.Contains(string(body), "<script") {
		t.Fatalf("error page is not escaped: %s", body)
	}
}
