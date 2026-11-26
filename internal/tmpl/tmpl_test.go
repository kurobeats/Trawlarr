package tmpl

import (
	"net/url"
	"strings"
	"testing"
)

func recipient() Recipient {
	return Recipient{
		FirstName: "Alice",
		LastName:  "Ng",
		Email:     "alice@example.com",
		Position:  "CFO",
		Custom:    map[string]string{"department": "Finance"},
	}
}

func TestContextFields(t *testing.T) {
	ctx, err := NewContext("IT Support <it@example.com>", "https://training.example.com", "rid123", recipient())
	if err != nil {
		t.Fatal(err)
	}
	if ctx.From != "IT Support" {
		t.Fatalf("From = %q", ctx.From)
	}
	if ctx.BaseURL != "https://training.example.com" {
		t.Fatalf("BaseURL = %q", ctx.BaseURL)
	}
	for name, got := range map[string]string{"URL": ctx.URL, "TrackingURL": ctx.TrackingURL} {
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("%s is not a URL: %v", name, err)
		}
		if u.Query().Get("rid") != "rid123" {
			t.Fatalf("%s missing rid: %s", name, got)
		}
		if u.Host != "training.example.com" {
			t.Fatalf("%s has wrong host: %s", name, got)
		}
	}
	if !strings.HasSuffix(ctx.URL, "/click?rid=rid123") {
		t.Fatalf("unexpected landing URL: %s", ctx.URL)
	}
	if !strings.Contains(ctx.Tracker, "<img") || !strings.Contains(ctx.Tracker, ctx.TrackingURL) {
		t.Fatalf("tracker markup is wrong: %s", ctx.Tracker)
	}
	if ctx.RId != "rid123" {
		t.Fatalf("RId = %q", ctx.RId)
	}
}

func TestFromFallsBackToAddress(t *testing.T) {
	ctx, err := NewContext("it@example.com", "https://x.example.com", "r", recipient())
	if err != nil {
		t.Fatal(err)
	}
	if ctx.From != "it@example.com" {
		t.Fatalf("From should be the bare address, got %q", ctx.From)
	}
}

func TestBaseURLDropsPathAndQuery(t *testing.T) {
	ctx, err := NewContext("it@example.com", "https://training.example.com/landing?campaign=9", "r", recipient())
	if err != nil {
		t.Fatal(err)
	}
	if ctx.BaseURL != "https://training.example.com" {
		t.Fatalf("BaseURL kept path or query: %q", ctx.BaseURL)
	}
	if !strings.Contains(ctx.URL, "/landing/click") {
		t.Fatalf("URL should keep the configured path: %s", ctx.URL)
	}
}

func TestPerRecipientURL(t *testing.T) {
	ctx, err := NewContext("it@example.com", "https://{{.Email}}.example.com", "r", recipient())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctx.URL, "alice@example.com") {
		t.Fatalf("template in the URL was not expanded: %s", ctx.URL)
	}
}

func TestExecuteRendersRecipient(t *testing.T) {
	ctx, _ := NewContext("IT <it@example.com>", "https://x.example.com", "rid", recipient())
	cases := map[string]string{
		"Hi {{.FirstName}}":                "Hi Alice",
		"{{.FirstName}} {{.LastName}}":     "Alice Ng",
		"{{.Email}} ({{.Position}})":       "alice@example.com (CFO)",
		"{{.FirstName}} at {{.BaseURL}}":   "Alice at https://x.example.com",
		"{{index .Custom \"department\"}}": "Finance",
		"no placeholders":                  "no placeholders",
		"{{.URL}}":                         ctx.URL,
	}
	for in, want := range cases {
		got, err := Execute(in, ctx)
		if err != nil {
			t.Fatalf("render %q: %v", in, err)
		}
		if got != want {
			t.Errorf("render %q = %q, want %q", in, got, want)
		}
	}
}

func TestExecuteEmptyAndInvalid(t *testing.T) {
	if got, err := Execute("", recipient()); err != nil || got != "" {
		t.Fatalf("empty template: %q %v", got, err)
	}
	if _, err := Execute("{{.FirstName", recipient()); err == nil {
		t.Fatal("unclosed action accepted")
	}
	if _, err := Execute("{{fail}}", recipient()); err == nil {
		t.Fatal("unknown function accepted")
	}
	// Unknown struct fields fail loudly rather than silently rendering empty,
	// which would have sent broken emails to a whole campaign.
	if _, err := Execute("{{.Missing}}", recipient()); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestNewContextRejectsBadBaseURL(t *testing.T) {
	// An unparseable base URL must not silently produce broken links.
	if _, err := NewContext("it@example.com", "http://[::1", "rid", recipient()); err == nil {
		t.Fatal("invalid base URL accepted")
	}
}
