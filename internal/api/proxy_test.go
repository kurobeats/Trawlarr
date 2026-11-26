package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kurobeats/trawlarr/internal/config"
	"github.com/kurobeats/trawlarr/internal/db"
	"github.com/kurobeats/trawlarr/internal/secret"
	"github.com/kurobeats/trawlarr/internal/store"
)

func TestRewriteLinks(t *testing.T) {
	base, _ := url.Parse("https://login.example.com")
	html := []byte(`<html><head><base href="/"><meta http-equiv="Content-Security-Policy" content="x"></head>
<body>
<a href="/inbox">relative</a>
<a href="https://login.example.com/account">absolute same host</a>
<a href="https://evil.example.net/x">third party</a>
<form action="/common/login"><input name="user"></form>
<img src="/logo.png"><img src="data:image/png;base64,AA">
<script src="https://cdn.example.org/x.js"></script>
</body></html>`)
	out := string(rewriteLinks(html, base, "rid123"))

	for _, want := range []string{
		`href="/px/rid123/inbox"`,
		`href="/px/rid123/account"`,
		`action="/px/rid123/common/login"`,
		`src="/px/rid123/logo.png"`,
		`href="https://evil.example.net/x"`,
		`src="https://cdn.example.org/x.js"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Content-Security-Policy") || strings.Contains(out, "<base") {
		t.Error("CSP or base tag survived rewriting")
	}
}

func TestSetCookieRewriting(t *testing.T) {
	raw := "ESTSAUTH=abc123; Domain=login.example.com; Path=/; Secure; HttpOnly; SameSite=Lax"
	out := rewriteSetCookie(raw)
	if strings.Contains(out, "Domain=") {
		t.Fatalf("domain not stripped: %s", out)
	}
	if !strings.Contains(out, "Path=/") {
		t.Fatalf("path not reset: %s", out)
	}
	if !strings.Contains(out, "SameSite=None") {
		t.Fatalf("samesite not relaxed: %s", out)
	}
	if !strings.Contains(out, "HttpOnly") {
		t.Fatalf("httponly dropped: %s", out)
	}
}

func TestParseSetCookie(t *testing.T) {
	if name, value, keep := parseSetCookie("a=b; Path=/"); name != "a" || value != "b" || !keep {
		t.Fatal("normal cookie not parsed")
	}
	if _, _, keep := parseSetCookie("a=; Max-Age=0"); keep {
		t.Fatal("deletion cookie should not be stored")
	}
	if _, _, keep := parseSetCookie("broken"); keep {
		t.Fatal("malformed cookie should be rejected")
	}
}

func TestRelayCapturesCookies(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/common/login" && r.Method == http.MethodPost {
			http.SetCookie(w, &http.Cookie{
				Name: "AUTH", Value: "session-token", Path: "/", Domain: "127.0.0.1", HttpOnly: true,
			})
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<html><body><a href="/inbox">Inbox</a></body></html>`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><body><form action="/common/login"><input name="user"></form></body></html>`)
	}))
	defer upstream.Close()

	conn, err := db.Open(t.TempDir() + "/relay.db")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	box, _ := secret.New(make([]byte, 32))
	st := store.New(conn, box)
	if _, err := st.Seed("admin", "hash"); err != nil {
		t.Fatal(err)
	}
	g := &store.Group{OrgID: 1, Name: "G", Targets: []store.Target{{Email: "victim@example.com"}}}
	if err := st.SaveGroup(g, 0); err != nil {
		t.Fatal(err)
	}
	parsedUpstream, _ := url.Parse(upstream.URL)
	host := parsedUpstream.Host
	c := &store.Campaign{OrgID: 1, Name: "Relay", GroupIDs: []int64{g.ID}, ProxyURL: upstream.URL,
		CaptureCookies: true, Status: store.StatusSending}
	if err := st.SaveCampaign(c, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MaterializeTargets(c); err != nil {
		t.Fatal(err)
	}
	targets, err := st.CampaignTargets(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := targets[0]

	cfg := &config.Config{ProxyEnabled: true, ProxyAllowlist: []string{parsedUpstream.Hostname()}}
	srv := New(st, nil, nil, nil, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	// Trust the test server's certificate.
	old := upstreamClient
	upstreamClient = upstream.Client()
	upstreamClient.CheckRedirect = old.CheckRedirect
	defer func() { upstreamClient = old }()

	// The landing page comes back rewritten.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/click?rid="+target.RID, nil)
	req.RemoteAddr = "192.0.2.10:1234"
	srv.relay(rec, req, &target, c, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `/px/`+target.RID+`/common/login`) {
		t.Fatalf("form action not rewritten: %s", rec.Body.String())
	}

	// Submitting the form captures the upstream cookie.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/px/"+target.RID+"/common/login",
		strings.NewReader("user=victim&pass=hunter2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.10:1234"
	srv.relay(rec, req, &target, c, "common/login")
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	cookies, err := st.ProxySession(target.RID, host)
	if err != nil {
		t.Fatalf("session not stored: %v", err)
	}
	if cookies["AUTH"] != "session-token" {
		t.Fatalf("cookie not captured: %+v", cookies)
	}
	if got := rec.Header().Get("Set-Cookie"); !strings.Contains(got, "Path=/") || strings.Contains(got, "Domain=") {
		t.Fatalf("Set-Cookie not rebound: %q", got)
	}
}

func TestRelayRefusesUnlistedHost(t *testing.T) {
	cfg := &config.Config{ProxyEnabled: true, ProxyAllowlist: []string{"example.com"}}
	srv := New(nil, nil, nil, nil, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	denied := []string{"https://not-example.com", "https://evil.com", "https://example.com.evil.net"}
	for _, u := range denied {
		parsed, _ := url.Parse(u)
		if cfg.ProxyHostAllowed(parsed.Hostname()) {
			t.Errorf("%s should be denied", u)
		}
	}
	if !cfg.ProxyHostAllowed("login.example.com") {
		t.Error("subdomain of an allowed host was refused")
	}
	if cfg.ProxyHostAllowed("example.com") && !cfg.ProxyEnabled {
		t.Error("disabled proxy allowed a host")
	}
	if (*config.Config)(&config.Config{}).ProxyHostAllowed("example.com") {
		t.Error("empty allowlist must deny")
	}
	_ = srv
}
