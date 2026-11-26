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
// httpsHost returns the hostname to redirect to: one we hold a profile for, or
// the first configured domain as a fallback.
func (h challengeHandler) httpsHost(requestHost string) string {
	host := strings.ToLower(strings.TrimSuffix(strings.Split(requestHost, ":")[0], "."))
	profiles := h.lookup()
	for _, p := range profiles {
		domains, err := NormaliseDomains(p.Domains)
		if err != nil {
			continue
		}
		for _, d := range domains {
			if d == host {
				return host
			}
		}
	}
	if len(profiles) > 0 {
		if domains, err := NormaliseDomains(profiles[0].Domains); err == nil && len(domains) > 0 {
			return domains[0]
		}
	}
	return ""
}

// validKeyAuth checks the shape of an ACME key authorization: token.thumbprint.
func validKeyAuth(raw []byte) bool {
	s := strings.TrimSpace(string(raw))
	if len(s) < 16 || len(s) > 512 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return strings.Contains(s, ".")
}

func (h challengeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const prefix = "/.well-known/acme-challenge/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		// Redirect to our own hostname, never to the Host header, which the
		// caller controls.
		host := h.httpsHost(r.Host)
		if host == "" {
			http.NotFound(w, r)
			return
		}
		// #nosec G710 -- host is one of our own profile domains, never r.Host.
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, prefix)
	// A token is a file name: reject anything that is not a plain base name.
	if token == "" || token != filepath.Base(token) || token == "." || token == ".." ||
		strings.ContainsAny(token, "/\\ \t\r\n") {
		http.NotFound(w, r)
		return
	}
	for _, profile := range h.lookup() {
		// #nosec G304 G703 -- token was reduced to a base name above.
		raw, err := os.ReadFile(filepath.Join(challengeDir(profile), token))
		if err != nil {
			continue
		}
		// Only ever serve a key authorization, so the response cannot carry
		// markup even if the file was tampered with.
		if !validKeyAuth(raw) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=us-ascii")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// #nosec G705 -- only an ACME key authorization passes validKeyAuth.
		w.Write(raw)
		return
	}
	http.NotFound(w, r)
}
