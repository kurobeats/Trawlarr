package api

import (
	"bytes"
	"io"
	"net/url"
	"testing"
)

// These run as ordinary unit tests against the seed corpus, and can be extended
// with `go test -fuzz=FuzzParseTargetCSV ./internal/api`.

func FuzzParseTargetCSV(f *testing.F) {
	f.Add("email,first_name\nA@example.com,Alice\n")
	f.Add("Alice,Ng,alice@example.com,CFO\n")
	f.Add("\xEF\xBB\xBFemail\nx@y.z\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, body string) {
		targets, err := ParseTargetCSV(io.NopCloser(bytes.NewReader([]byte(body))))
		if err != nil {
			return
		}
		for _, target := range targets {
			if target.Email == "" {
				t.Fatal("empty email accepted")
			}
		}
	})
}

func FuzzSetCookieParsing(f *testing.F) {
	f.Add("AUTH=abc; Domain=x; Path=/; Secure; HttpOnly")
	f.Add("a=; Max-Age=0")
	f.Add("broken")
	f.Fuzz(func(t *testing.T, raw string) {
		name, value, keep := parseSetCookie(raw)
		if keep && name == "" {
			t.Fatal("kept a cookie without a name")
		}
		if len(name) > 256 || len(value) > 8192 {
			t.Fatal("oversized cookie accepted")
		}
		_ = rewriteSetCookie(raw)
	})
}

func FuzzRewriteLinks(f *testing.F) {
	base, _ := url.Parse("https://login.example.com")
	f.Add(`<a href="/x">y</a>`)
	f.Add(`<form action="https://login.example.com/login"></form>`)
	f.Add(`<img src="//cdn.example.com/a.png">`)
	f.Add("not html at all <")
	f.Fuzz(func(t *testing.T, html string) {
		out := rewriteLinks([]byte(html), base, "rid123")
		if len(out) == 0 && html != "" {
			t.Fatal("rewrite produced empty output")
		}
	})
}
