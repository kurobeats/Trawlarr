package tlsutil

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// httpHandler is deliberately the narrowest interface the challenge server
// needs, which keeps the ACME logic testable without a server.
type httpHandler interface {
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}

type challengeHandler struct{ lookup func() []Profile }

// ServeHTTP answers /.well-known/acme-challenge/<token> from disk and sends
// everything else to HTTPS.
func (h challengeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const prefix = "/.well-known/acme-challenge/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		target := "https://" + r.Host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, prefix)
	if token == "" || strings.ContainsAny(token, "/ \t\r\n") {
		http.NotFound(w, r)
		return
	}
	for _, profile := range h.lookup() {
		raw, err := os.ReadFile(filepath.Join(challengeDir(profile), token))
		if err != nil {
			continue
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write(raw)
		return
	}
	http.NotFound(w, r)
}
