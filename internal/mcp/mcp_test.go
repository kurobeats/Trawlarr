package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kurobeats/trawlarr/internal/ai"
	"github.com/kurobeats/trawlarr/internal/config"
	"github.com/kurobeats/trawlarr/internal/db"
	"github.com/kurobeats/trawlarr/internal/secret"
	"github.com/kurobeats/trawlarr/internal/store"
)

// session drives a real stdio MCP server over pipes.
type session struct {
	t      *testing.T
	st     *store.Store
	srv    *Server
	in     *io.PipeWriter
	out    *bufio.Scanner
	cancel context.CancelFunc
	nextID int
	mu     sync.Mutex
}

func newSession(t *testing.T, tweak func(*config.Config)) *session {
	t.Helper()
	conn, err := db.Open(t.TempDir() + "/mcp.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	box, _ := secret.New(make([]byte, 32))
	st := store.New(conn, box)
	if _, err := st.Seed("admin", "hash"); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Enabled = true
	cfg.AI.Enabled = true
	cfg.AI.BaseURL = "http://ai.invalid/v1"
	cfg.AI.Model = "test"
	if tweak != nil {
		tweak(cfg)
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	deps := Deps{Store: st, Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if cfg.AI.BaseURL != "" {
		// Tests that need AI replace it explicitly.
		deps.AI = ai.New(cfg.AI)
	}
	srv, err := NewWithIO(deps, inR, outW)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	t.Cleanup(cancel)

	out := bufio.NewScanner(outR)
	out.Buffer(make([]byte, 0, 64*1024), 8<<20)
	s := &session{t: t, st: st, srv: srv, in: inW, out: out, cancel: cancel}
	s.mustRPC("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "1"},
	})
	s.notify("notifications/initialized")
	return s
}

// send writes one JSON-RPC message.
func (s *session) send(msg map[string]any) {
	s.t.Helper()
	b, err := json.Marshal(msg)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.in.Write(append(b, '\n')); err != nil {
		s.t.Fatalf("write: %v", err)
	}
}

// read returns the next response line.
func (s *session) read() map[string]any {
	s.t.Helper()
	if !s.out.Scan() {
		s.t.Fatalf("no response from the MCP server: %v", s.out.Err())
	}
	var decoded map[string]any
	if err := json.Unmarshal(s.out.Bytes(), &decoded); err != nil {
		s.t.Fatalf("invalid response %q: %v", s.out.Bytes(), err)
	}
	return decoded
}

func (s *session) notify(method string) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": method})
}

// rpc sends a request and returns the response.
func (s *session) rpc(method string, params any) map[string]any {
	s.t.Helper()
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.mu.Unlock()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	s.send(msg)
	return s.read()
}

func (s *session) mustRPC(method string, params any) map[string]any {
	s.t.Helper()
	resp := s.rpc(method, params)
	if resp["error"] != nil {
		s.t.Fatalf("%s failed: %v", method, resp["error"])
	}
	return resp
}

// call invokes a tool and returns the text content.
func (s *session) call(name string, args map[string]any) (string, bool) {
	s.t.Helper()
	resp := s.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	return toolText(resp)
}

// mustCall invokes a tool and requires success.
func (s *session) mustCall(name string, args map[string]any) string {
	s.t.Helper()
	text, failed := s.call(name, args)
	if failed {
		s.t.Fatalf("%s failed: %s", name, text)
	}
	return text
}

// toolText extracts the response text and whether the call was an error.
func toolText(resp map[string]any) (string, bool) {
	if err := resp["error"]; err != nil {
		if m, ok := err.(map[string]any); ok {
			if s, ok := m["message"].(string); ok {
				return s, true
			}
		}
		return "json-rpc error", true
	}
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		return "", true
	}
	if isErr, _ := result["isError"].(bool); isErr {
		return contentText(result), true
	}
	return contentText(result), false
}

func contentText(result map[string]any) string {
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}

// seedLibrary creates a group, template, page and profile through the tools.
func (s *session) seedLibrary() {
	s.t.Helper()
	s.mustCall("create_group", map[string]any{
		"name": "Execs",
		"csv":  "email,first_name,last_name\nalice@example.com,Alice,Ng\nbob@example.com,Bob,Lee\n",
	})
	s.mustCall("create_template", map[string]any{
		"name": "Invoice", "subject": "Hi {{.FirstName}}",
		"html_body": `<a href="{{.URL}}">pay</a>{{.Tracker}}`,
	})
	s.mustCall("create_landing_page", map[string]any{
		"name": "Portal", "html": `<form><input name="username"><input name="password" type="password"></form>`,
		"capture": true, "redirect_url": "https://example.com/thanks",
	})
	// Sending profiles are only creatable through the HTTP API today, so write
	// one directly for the MCP tests.
	p := &store.Profile{OrgID: 1, Name: "Relay", Host: "127.0.0.1", Port: 2525, FromAddress: "IT <it@example.com>"}
	if err := s.st.SaveProfile(p, 0); err != nil {
		s.t.Fatal(err)
	}
}

func TestInitializeAndToolList(t *testing.T) {
	s := newSession(t, nil)
	resp := s.mustRPC("tools/list", nil)
	result := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) < 10 {
		t.Fatalf("expected the full tool set, got %d", len(tools))
	}
	names := map[string]bool{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		names[tool["name"].(string)] = true
		if tool["description"] == "" || tool["description"] == nil {
			t.Errorf("%v has no description", tool["name"])
		}
		if tool["inputSchema"] == nil {
			t.Errorf("%v has no input schema", tool["name"])
		}
	}
	for _, want := range []string{"list_campaigns", "get_campaign", "dashboard", "create_group", "launch_campaign", "draft_email"} {
		if !names[want] {
			t.Errorf("tool %s not registered", want)
		}
	}
}

func TestReadTools(t *testing.T) {
	s := newSession(t, func(c *config.Config) { c.MCP.AllowWrites = true })
	s.seedLibrary()

	groups := s.mustCall("list_groups", map[string]any{"include_targets": true})
	if !strings.Contains(groups, "alice@example.com") {
		t.Fatalf("group recipients missing: %s", groups)
	}
	templates := s.mustCall("list_templates", map[string]any{})
	if !strings.Contains(templates, "Invoice") {
		t.Fatalf("template missing: %s", templates)
	}
	pages := s.mustCall("list_landing_pages", map[string]any{})
	if !strings.Contains(pages, "Portal") {
		t.Fatalf("page missing: %s", pages)
	}
	profiles := s.mustCall("list_sending_profiles", map[string]any{})
	if !strings.Contains(profiles, "Relay") {
		t.Fatalf("profile missing: %s", profiles)
	}
	if strings.Contains(profiles, "password") {
		t.Fatalf("profile tool leaked credentials: %s", profiles)
	}
	dash := s.mustCall("dashboard", map[string]any{})
	if !strings.Contains(dash, "library") {
		t.Fatalf("dashboard output unexpected: %s", dash)
	}
}

func TestCreateCampaignAndQueueIt(t *testing.T) {
	s := newSession(t, func(c *config.Config) { c.MCP.AllowWrites = true })
	s.seedLibrary()

	created := s.mustCall("create_campaign", map[string]any{
		"name": "Q4", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
	})
	if !strings.Contains(created, `"status": "draft"`) {
		t.Fatalf("campaign not created as a draft: %s", created)
	}
	if status, _ := s.st.CampaignStatus(1); status != store.StatusDraft {
		t.Fatalf("status in the database = %q", status)
	}

	launched := s.mustCall("launch_campaign", map[string]any{"id": 1})
	if !strings.Contains(launched, `"queued": true`) {
		t.Fatalf("launch did not queue the campaign: %s", launched)
	}
	c, err := s.st.Campaign(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != store.StatusQueued {
		t.Fatalf("campaign status = %q, want queued", c.Status)
	}
	if c.LaunchAt.IsZero() {
		t.Fatal("launch_at not set, the sending server would never pick it up")
	}
	if n, err := s.st.CampaignTargets(1); err != nil || len(n) != 2 {
		t.Fatalf("recipients not materialised: %d %v", len(n), err)
	}

	stopped := s.mustCall("stop_campaign", map[string]any{"id": 1})
	if !strings.Contains(stopped, `"stopped": true`) {
		t.Fatalf("stop failed: %s", stopped)
	}
	if status, _ := s.st.CampaignStatus(1); status != store.StatusStopped {
		t.Fatalf("status after stop = %q", status)
	}
}

func TestGetCampaignIncludesAudience(t *testing.T) {
	s := newSession(t, func(c *config.Config) { c.MCP.AllowWrites = true })
	s.seedLibrary()
	s.mustCall("create_campaign", map[string]any{
		"name": "Q4", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
	})
	s.mustCall("launch_campaign", map[string]any{"id": 1})

	detail := s.mustCall("get_campaign", map[string]any{"id": 1, "include_recipients": true})
	if !strings.Contains(detail, "alice@example.com") {
		t.Fatalf("recipients missing from detail: %s", detail)
	}
	csv := s.mustCall("export_campaign", map[string]any{"id": 1})
	if !strings.HasPrefix(csv, "email,first_name") || !strings.Contains(csv, "alice@example.com") {
		t.Fatalf("unexpected CSV export: %s", csv)
	}
}

func TestWritesAreBlockedByDefault(t *testing.T) {
	s := newSession(t, func(c *config.Config) { c.MCP.AllowWrites = false })
	text, failed := s.call("create_group", map[string]any{"name": "x", "csv": "email\na@example.com\n"})
	if !failed || !strings.Contains(text, "allow_writes") {
		t.Fatalf("write was not blocked: %q (failed=%v)", text, failed)
	}
	if groups, _ := s.st.Groups(1); len(groups) != 0 {
		t.Fatal("blocked write still created a group")
	}
	// Reads still work.
	if _, failed := s.call("list_campaigns", map[string]any{}); failed {
		t.Fatal("read tool failed while writes were disabled")
	}
}

func TestUnknownToolAndBadArguments(t *testing.T) {
	s := newSession(t, func(c *config.Config) { c.MCP.AllowWrites = true })
	if _, failed := s.call("delete_everything", map[string]any{}); !failed {
		t.Fatal("unknown tool accepted")
	}
	if text, failed := s.call("get_campaign", map[string]any{"id": 999}); !failed ||
		!strings.Contains(text, "not found") {
		t.Fatalf("missing campaign not reported: %q", text)
	}
	if _, failed := s.call("create_campaign", map[string]any{"name": "x"}); !failed {
		t.Fatal("campaign created without references")
	}
}

func TestAITools(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		user := ""
		for _, m := range body.Messages {
			if m.Role == "user" {
				user = m.Content
			}
		}
		content := `{"subject":"Payroll","text_body":"hi","html_body":"<p>hi</p>","name":"Portal","html":"<form><input name=\"username\"></form>"}`
		if strings.Contains(user, "Campaign:") {
			content = "Risk is elevated: 40% clicked. Re-train finance."
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
		})
	}))
	defer upstream.Close()

	s := newSession(t, func(c *config.Config) {
		c.MCP.AllowWrites = true
		c.AI.BaseURL = upstream.URL
	})
	s.srv.ai = ai.New(config.AI{Enabled: true, BaseURL: upstream.URL, Model: "m"})

	draft := s.mustCall("draft_email", map[string]any{"prompt": "payroll reminder"})
	if !strings.Contains(draft, "Payroll") {
		t.Fatalf("unexpected draft: %s", draft)
	}
	page := s.mustCall("draft_landing_page", map[string]any{"prompt": "portal login"})
	if !strings.Contains(page, "<form") {
		t.Fatalf("unexpected page draft: %s", page)
	}

	s.seedLibrary()
	s.mustCall("create_campaign", map[string]any{
		"name": "Q4", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
	})
	analysis := s.mustCall("analyze_campaign", map[string]any{"id": 1})
	if !strings.Contains(analysis, "Risk is elevated") {
		t.Fatalf("unexpected analysis: %s", analysis)
	}
}

func TestAIDisabled(t *testing.T) {
	s := newSession(t, func(c *config.Config) { c.AI.Enabled = false })
	s.srv.ai = nil
	for _, tool := range []string{"draft_email", "analyze_campaign"} {
		text, failed := s.call(tool, map[string]any{"prompt": "x", "id": 1})
		if !failed || !strings.Contains(text, "disabled") {
			t.Fatalf("%s should report that AI is disabled: %q", tool, text)
		}
	}
}

func TestActorSelection(t *testing.T) {
	// Configuring a missing user is a hard error rather than a silent fallback.
	conn, err := db.Open(t.TempDir() + "/actor.db")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	box, _ := secret.New(make([]byte, 32))
	st := store.New(conn, box)
	if _, err := st.Seed("admin", "hash"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.MCP.Enabled = true
	cfg.MCP.User = "ghost"
	if _, err := New(Deps{Store: st, Cfg: cfg}); err == nil {
		t.Fatal("unknown MCP user accepted")
	}
	cfg.MCP.User = "admin"
	srv, err := New(Deps{Store: st, Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if srv.Actor().Username != "admin" {
		t.Fatalf("actor = %q", srv.Actor().Username)
	}
}

func TestDisabledServerRefusesToStart(t *testing.T) {
	conn, err := db.Open(t.TempDir() + "/off.db")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	box, _ := secret.New(make([]byte, 32))
	st := store.New(conn, box)
	if _, err := st.Seed("admin", "hash"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default() // mcp disabled
	if _, err := New(Deps{Store: st, Cfg: cfg}); err == nil {
		t.Fatal("disabled MCP server started")
	}
}

func TestAuditTrail(t *testing.T) {
	s := newSession(t, func(c *config.Config) { c.MCP.AllowWrites = true })
	s.seedLibrary()
	entries, err := s.st.AuditLog(1, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "mcp.create_group" {
			found = true
			if e.IP != "stdio" {
				t.Fatalf("unexpected audit source %q", e.IP)
			}
		}
	}
	if !found {
		t.Fatal("MCP tool call was not audited")
	}
}

func TestVersionHandshake(t *testing.T) {
	SetVersion("9.9.9")
	s := newSession(t, nil)
	resp := s.rpc("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "t", "version": "1"}})
	info := resp["result"].(map[string]any)["serverInfo"].(map[string]any)
	if info["version"] != "9.9.9" || info["name"] != "trawlarr" {
		t.Fatalf("unexpected server info: %v", info)
	}
}

// keep time imported for helpers above
var _ = time.Now

func TestCreateCampaignValidation(t *testing.T) {
	s := newSession(t, func(c *config.Config) { c.MCP.AllowWrites = true })
	s.seedLibrary()

	if text, failed := s.call("create_campaign", map[string]any{
		"name": "bad-template", "template_id": 99, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
	}); !failed || !strings.Contains(text, "template") {
		t.Fatalf("missing template not reported: %q", text)
	}
	if text, failed := s.call("create_campaign", map[string]any{
		"name": "bad-page", "template_id": 1, "page_id": 99, "profile_id": 1, "group_ids": []int{1},
	}); !failed || !strings.Contains(text, "landing page") {
		t.Fatalf("missing page not reported: %q", text)
	}
	if text, failed := s.call("create_campaign", map[string]any{
		"name": "bad-profile", "template_id": 1, "page_id": 1, "profile_id": 99, "group_ids": []int{1},
	}); !failed || !strings.Contains(text, "profile") {
		t.Fatalf("missing profile not reported: %q", text)
	}
	if text, failed := s.call("create_campaign", map[string]any{
		"name": "bad-group", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{99},
	}); !failed || !strings.Contains(text, "group") {
		t.Fatalf("missing group not reported: %q", text)
	}
	if text, failed := s.call("create_campaign", map[string]any{
		"name": "bad-date", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
		"launch_at": "tomorrow",
	}); !failed || !strings.Contains(text, "RFC3339") {
		t.Fatalf("bad launch_at not reported: %q", text)
	}

	// A scheduled draft keeps the timestamp.
	created := s.mustCall("create_campaign", map[string]any{
		"name": "scheduled", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
		"launch_at": "2030-01-02T09:00:00Z", "send_rate": 10,
	})
	if !strings.Contains(created, "2030-01-02") {
		t.Fatalf("launch_at not stored: %s", created)
	}
}

func TestReadToolsOnEmptyDatabase(t *testing.T) {
	s := newSession(t, nil)
	for _, tool := range []string{"dashboard", "list_campaigns", "list_groups", "list_templates", "list_landing_pages", "list_sending_profiles"} {
		if text, failed := s.call(tool, map[string]any{}); failed {
			t.Errorf("%s failed on an empty database: %s", tool, text)
		}
	}
	if text, failed := s.call("get_campaign", map[string]any{"id": 1}); !failed || !strings.Contains(text, "not found") {
		t.Fatalf("missing campaign = %q", text)
	}
	if text, failed := s.call("export_campaign", map[string]any{"id": 1}); !failed || !strings.Contains(text, "not found") {
		t.Fatalf("export of a missing campaign = %q", text)
	}
	if text, failed := s.call("get_template", map[string]any{"id": 1}); !failed || !strings.Contains(text, "not found") {
		t.Fatalf("missing template = %q", text)
	}
}
