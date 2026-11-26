package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCloneURLAssumesHTTPSThenFallsBack(t *testing.T) {
	// A plain-HTTP server: "https://host" fails the handshake, "http://host" works.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	plain := newUpstreamPage(t)
	bare := strings.TrimPrefix(plain, "http://")

	html, source, err := FetchLandingPage(t.Context(), bare)
	if err != nil {
		t.Fatalf("fallback failed: %v", err)
	}
	if !strings.Contains(html, "<form") {
		t.Fatalf("unexpected html: %s", html)
	}
	if !strings.HasPrefix(source, "http://") {
		t.Fatalf("source = %q, want the working http scheme", source)
	}
}

func TestCloneRejectsBadInput(t *testing.T) {
	for _, raw := range []string{"", "   ", "ftp://example.com", "not a url"} {
		if _, _, err := FetchLandingPage(t.Context(), raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

func TestCloneReportsBothAttempts(t *testing.T) {
	// Nothing listens here, so https and http both fail.
	_, _, err := FetchLandingPage(t.Context(), "127.0.0.1:1")
	if err == nil {
		t.Fatal("dead host accepted")
	}
	if !strings.Contains(err.Error(), "https or http") {
		t.Fatalf("error does not explain the fallback: %v", err)
	}
}
