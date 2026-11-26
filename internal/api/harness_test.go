package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kurobeats/trawlarr/internal/ai"
	"github.com/kurobeats/trawlarr/internal/auth"
	"github.com/kurobeats/trawlarr/internal/config"
	"github.com/kurobeats/trawlarr/internal/db"
	"github.com/kurobeats/trawlarr/internal/engine"
	"github.com/kurobeats/trawlarr/internal/mail"
	"github.com/kurobeats/trawlarr/internal/secret"
	"github.com/kurobeats/trawlarr/internal/store"
)

const testPassword = "test-password-123"

// errFake is a definite delivery failure used by sender tests.
var errFake = errors.New("connection refused")

// fakeSender records what the engine would have sent.
type fakeSender struct {
	sent []mail.Message
	err  error
}

func (f *fakeSender) Send(_ context.Context, _ *store.Profile, m mail.Message) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

type harness struct {
	t      *testing.T
	st     *store.Store
	srv    *Server
	cfg    *config.Config
	sender *fakeSender
	admin  *httptest.Server
	phish  *httptest.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	conn, err := db.Open(t.TempDir() + "/api.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(conn, box)
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Seed("admin", hash); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.PhishServer.PublicURL = "https://phish.test"
	cfg.AdminServer.ListenURL = "127.0.0.1:0"
	cfg.SessionTTLHours = 1
	cfg.TrustedOrigins = []string{"https://console.test"}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sender := &fakeSender{}
	eng := engine.New(st, sender, nil, log, func(c *store.Campaign) string {
		if c.URL != "" {
			return c.URL
		}
		if v, _ := st.Setting("phish_public_url"); v != "" {
			return v
		}
		return cfg.PhishServer.BaseURL()
	})
	ui := fstest.MapFS{
		"index.html":     &fstest.MapFile{Data: []byte("<!doctype html><title>Trawlarr</title>")},
		"assets/app.js":  &fstest.MapFile{Data: []byte("console.log(1)")},
		"assets/app.css": &fstest.MapFile{Data: []byte("body{}")},
	}
	srv := New(st, eng, sender, nil, cfg, log, fs.FS(ui))

	h := &harness{t: t, st: st, srv: srv, cfg: cfg, sender: sender}
	h.admin = httptest.NewServer(srv.AdminHandler())
	h.phish = httptest.NewServer(srv.PhishHandler())
	t.Cleanup(func() {
		h.admin.Close()
		h.phish.Close()
	})
	return h
}

// client returns an HTTP client with its own cookie jar.
func (h *harness) client() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

// noRedirect returns a client that reports redirects instead of following them.
func (h *harness) noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// postForm submits an application/x-www-form-urlencoded body.
func (h *harness) postForm(c *http.Client, url string, values url.Values) (int, []byte) {
	h.t.Helper()
	if c == nil {
		c = http.DefaultClient
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(values.Encode()))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// login authenticates and returns the authenticated client.
func (h *harness) login() *http.Client {
	h.t.Helper()
	c := h.client()
	status, body := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/login", map[string]string{
		"username": "admin", "password": testPassword,
	})
	if status != http.StatusOK {
		h.t.Fatalf("login failed: %d %s", status, body)
	}
	return c
}

// do performs a request, JSON-encoding non-nil maps and raw []byte bodies.
func (h *harness) do(c *http.Client, method, url string, body any) (int, []byte) {
	h.t.Helper()
	if c == nil {
		c = http.DefaultClient // public tracking endpoints need no session
	}
	var reader io.Reader
	contentType := ""
	switch v := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(v)
		contentType = "application/octet-stream"
	case io.Reader:
		reader = v
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
		contentType = "application/json"
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp.StatusCode, out
}

// json performs a request and decodes the JSON response.
func (h *harness) json(c *http.Client, method, url string, body any) (int, map[string]any) {
	h.t.Helper()
	status, raw := h.do(c, method, url, body)
	decoded := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			h.t.Fatalf("%s %s returned non-JSON (%d): %s", method, url, status, raw)
		}
	}
	return status, decoded
}

// adminDo performs an authenticated admin request.
func (h *harness) adminDo(c *http.Client, method, path string, body any) (int, []byte) {
	return h.do(c, method, h.admin.URL+path, body)
}

// expect asserts a status code and returns the decoded body.
func (h *harness) expect(c *http.Client, method, path string, body any, want int) map[string]any {
	h.t.Helper()
	status, decoded := h.json(c, method, h.admin.URL+path, body)
	if status != want {
		h.t.Fatalf("%s %s = %d, want %d: %v", method, path, status, want, decoded)
	}
	return decoded
}

// seedCampaign creates a group, template, page, profile and campaign through
// the API and returns the campaign id.
func (h *harness) seedCampaign(t *testing.T, c *http.Client) int64 {
	t.Helper()
	h.expect(c, http.MethodPost, "/api/v1/groups", map[string]any{
		"name": "Exec",
		"targets": []map[string]string{
			{"email": "alice@example.com", "first_name": "Alice", "last_name": "Ng"},
			{"email": "bob@example.com", "first_name": "Bob"},
		},
	}, http.StatusCreated)
	h.expect(c, http.MethodPost, "/api/v1/templates", map[string]any{
		"name":      "Invoice",
		"subject":   "Invoice for {{.FirstName}}",
		"text_body": "Hi {{.FirstName}}, see {{.URL}}",
		"html_body": `<p>Hi {{.FirstName}}</p><a href="{{.URL}}">Pay</a>{{.Tracker}}`,
	}, http.StatusCreated)
	h.expect(c, http.MethodPost, "/api/v1/pages", map[string]any{
		"name":              "Portal",
		"html":              `<html><body><form><input name="username"><input name="password" type="password"><button>Sign in</button></form></body></html>`,
		"capture":           true,
		"capture_values":    false,
		"capture_passwords": false,
		"redirect_url":      "https://example.com/thanks",
	}, http.StatusCreated)
	h.expect(c, http.MethodPost, "/api/v1/profiles", map[string]any{
		"name": "Relay", "host": "127.0.0.1", "port": 2525,
		"from_address": "IT <it@example.com>",
	}, http.StatusCreated)
	created := h.expect(c, http.MethodPost, "/api/v1/campaigns", map[string]any{
		"name": "Q4", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
		"url": "https://phish.test",
	}, http.StatusCreated)
	return int64(created["id"].(float64))
}

// expectRaw asserts a status code and returns the raw body.
func (h *harness) expectRaw(c *http.Client, method, path string, body any, want int) []byte {
	h.t.Helper()
	status, raw := h.adminDo(c, method, path, body)
	if status != want {
		h.t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, raw)
	}
	return raw
}

// newUpstreamPage serves a page with relative assets for the import test.
func newUpstreamPage(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<!doctype html><html><head>
<link rel="stylesheet" href="/static/app.css" integrity="sha384-x" crossorigin="anonymous">
</head><body>
<form action="/login"><input name="username"><input name="password" type="password"></form>
<img src="/static/logo.png">
</body></html>`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// campaignDetail fetches a campaign and its results.
func (h *harness) campaignDetail(c *http.Client, id int64) map[string]any {
	h.t.Helper()
	return h.expect(c, http.MethodGet, "/api/v1/campaigns/"+itoa(id), nil, http.StatusOK)
}

func itoa(v int64) string {
	return jsonNumber(v)
}

func jsonNumber(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// fakeAIClient points the server at a stub OpenAI-compatible endpoint.
func fakeAIClient(t *testing.T) *ai.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": `{"subject":"S","text_body":"t","html_body":"<p>t</p>","name":"Portal","html":"<form><input name=\"username\"></form>"}`,
			}}},
		})
	}))
	t.Cleanup(srv.Close)
	return ai.New(config.AI{Enabled: true, BaseURL: srv.URL, Model: "stub"})
}
