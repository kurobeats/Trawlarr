package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kurobeats/trawlarr/internal/config"
)

func fakeEndpoint(t *testing.T, handler http.HandlerFunc) (*httptest.Server, config.AI) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, config.AI{Enabled: true, BaseURL: srv.URL + "/v1", Model: "test-model", APIKey: "k"}
}

func completion(content string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
		})
	}
}

func TestNewRequiresConfiguration(t *testing.T) {
	if New(config.AI{Enabled: true}) != nil {
		t.Fatal("client created without a base URL or model")
	}
	if New(config.AI{BaseURL: "http://x", Model: "m"}) != nil {
		t.Fatal("client created while disabled")
	}
	c := New(config.AI{Enabled: true, BaseURL: "http://x/v1/", Model: "m"})
	if c == nil || c.Model() != "m" {
		t.Fatal("client not created for a complete config")
	}
}

func TestDraftEmailParsesFencedJSON(t *testing.T) {
	_, cfg := fakeEndpoint(t, completion("```json\n{\"subject\":\"Payroll update\",\"text_body\":\"Hi {{.FirstName}}\",\"html_body\":\"<p>Hi {{.FirstName}} <a href=\\\"{{.URL}}\\\">here</a>{{.Tracker}}</p>\"}\n```"))
	c := New(cfg)
	draft, err := c.DraftEmail(context.Background(), Request{Prompt: "payroll reminder"})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Subject != "Payroll update" {
		t.Fatalf("subject = %q", draft.Subject)
	}
	if !strings.Contains(draft.HTMLBody, "{{.URL}}") || !strings.Contains(draft.HTMLBody, "{{.Tracker}}") {
		t.Fatalf("placeholders lost: %s", draft.HTMLBody)
	}
}

func TestDraftEmailFallsBackToTextBody(t *testing.T) {
	_, cfg := fakeEndpoint(t, completion(`{"subject":"s","text_body":"line1\nline2","html_body":""}`))
	draft, err := New(cfg).DraftEmail(context.Background(), Request{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(draft.HTMLBody, "line1<br>line2") {
		t.Fatalf("no HTML fallback: %q", draft.HTMLBody)
	}
}

func TestDraftPageRequiresForm(t *testing.T) {
	_, cfg := fakeEndpoint(t, completion(`{"name":"Portal","html":"<html><body>no form here</body></html>"}`))
	if _, err := New(cfg).DraftPage(context.Background(), Request{Prompt: "portal"}); err == nil {
		t.Fatal("page without a form accepted")
	}

	_, cfg = fakeEndpoint(t, completion(`{"name":"Portal","html":"<html><body><form><input name=\"username\"><input name=\"password\" type=\"password\"></form></body></html>"}`))
	draft, err := New(cfg).DraftPage(context.Background(), Request{Prompt: "portal"})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Name != "Portal" || !strings.Contains(draft.HTML, "<form") {
		t.Fatalf("unexpected draft: %+v", draft)
	}
}

func TestErrorsAreReported(t *testing.T) {
	_, cfg := fakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
	})
	_, err := New(cfg).DraftEmail(context.Background(), Request{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("status error not surfaced: %v", err)
	}

	_, cfg = fakeEndpoint(t, completion("I cannot help with that."))
	if _, err := New(cfg).DraftEmail(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("prose response accepted as JSON")
	}

	_, cfg = fakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[]}`))
	})
	if _, err := New(cfg).DraftEmail(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("empty choices accepted")
	}
}

func TestPromptLimit(t *testing.T) {
	_, cfg := fakeEndpoint(t, completion(`{"subject":"s"}`))
	_, err := New(cfg).DraftEmail(context.Background(), Request{Prompt: strings.Repeat("a", MaxPromptLength+1)})
	if err == nil {
		t.Fatal("oversized prompt was forwarded")
	}
}

func TestTimeoutIsBounded(t *testing.T) {
	slow, cfg := fakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		completion(`{"subject":"late"}`)(w, r)
	})
	defer slow.Close()
	cfg.TimeoutSeconds = 1
	start := time.Now()
	if _, err := New(cfg).DraftEmail(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("expected the client timeout to fire")
	}
	if elapsed := time.Since(start); elapsed > 1300*time.Millisecond {
		t.Fatalf("timeout took %s, the client timeout was not applied", elapsed)
	}

	fast, cfg := fakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		completion(`{"subject":"ok"}`)(w, r)
	})
	defer fast.Close()
	if _, err := New(cfg).DraftEmail(context.Background(), Request{Prompt: "x"}); err != nil {
		t.Fatalf("request inside the timeout failed: %v", err)
	}
}

func TestAuthHeaderSent(t *testing.T) {
	var gotAuth string
	_, cfg := fakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		completion(`{"subject":"s"}`)(w, r)
	})
	if _, err := New(cfg).DraftEmail(context.Background(), Request{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer k" {
		t.Fatalf("authorization header = %q", gotAuth)
	}
}

func TestAPIKeyFromEnvironment(t *testing.T) {
	var gotAuth string
	_, cfg := fakeEndpoint(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		completion(`{"subject":"s"}`)(w, r)
	})
	cfg.APIKey = ""
	t.Setenv("TRAWLARR_AI_API_KEY", "from-env")
	if _, err := New(cfg).DraftEmail(context.Background(), Request{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer from-env" {
		t.Fatalf("environment key not used: %q", gotAuth)
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`: `{"a":1}`,
		"here you go:\n```json\n{\"a\":1}\n```\nthanks": `{"a":1}`,
		"prefix {\"a\":1} suffix":                       `{"a":1}`,
	}
	for in, want := range cases {
		if got := string(extractJSON(in)); got != want {
			t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}
