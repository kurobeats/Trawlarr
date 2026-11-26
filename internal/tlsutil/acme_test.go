package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeSelfSigned(t *testing.T, dir, domain string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "R3"},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(60 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	body := append(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, domain+".pem"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNormaliseDomains(t *testing.T) {
	got, err := NormaliseDomains([]string{"Training.Example.com, www.training.example.com", "training.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"training.example.com", "www.training.example.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"https://example.com", "example.com:443", "example.com/path", "localhost:8080"} {
		if _, err := NormaliseDomains([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got, _ := NormaliseDomains([]string{"localhost"}); len(got) != 1 {
		t.Fatal("localhost should be allowed for testing")
	}
}

func TestServerConfigLoadsCachedCert(t *testing.T) {
	dir := t.TempDir()
	cfg := Profile{Name: "p", Domains: []string{"training.example.com"}, AcceptTOS: true, CacheDir: dir}
	profiles := func() []Profile { return []Profile{cfg} }
	tlsCfg := ServerConfig(profiles)
	if _, err := tlsCfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "training.example.com"}); err == nil {
		t.Fatal("missing certificate did not fail the handshake")
	} else if !strings.Contains(err.Error(), "request one under Certificates") {
		t.Fatalf("unhelpful error: %v", err)
	}

	writeSelfSigned(t, dir, "training.example.com")
	cert, err := tlsCfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "training.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("empty certificate")
	}

	// A later issuance replaces the file and is picked up without a restart.
	time.Sleep(20 * time.Millisecond)
	writeSelfSigned(t, dir, "training.example.com")
	if _, err := tlsCfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "training.example.com"}); err != nil {
		t.Fatalf("reload after renewal: %v", err)
	}
}

func TestReadStatus(t *testing.T) {
	dir := t.TempDir()
	cfg := Profile{Name: "p", Domains: []string{"training.example.com"}, AcceptTOS: true, CacheDir: dir}
	empty := ReadStatus(cfg)
	if len(empty.Certificates) != 0 || empty.Error != "" {
		t.Fatalf("unexpected empty status: %+v", empty)
	}
	if empty.HTTPAddr != ":80" {
		t.Fatalf("challenge address = %q", empty.HTTPAddr)
	}

	writeSelfSigned(t, dir, "training.example.com")
	st := ReadStatus(cfg)
	if len(st.Certificates) != 1 {
		t.Fatalf("certificates = %+v", st.Certificates)
	}
	c := st.Certificates[0]
	if c.Domain != "training.example.com" || c.Issuer != "R3" {
		t.Fatalf("unexpected certificate: %+v", c)
	}
	if c.NotAfter.Time.Before(time.Now()) {
		t.Fatal("expiry not read")
	}
	if st.RenewsAt.Time.After(c.NotAfter.Time) || st.RenewsAt.IsZero() {
		t.Fatalf("renewal time = %v (expires %v)", st.RenewsAt, c.NotAfter)
	}
}

func TestChallengeHandler(t *testing.T) {
	dir := t.TempDir()
	cfg := Profile{Name: "p", Domains: []string{"training.example.com"}, AcceptTOS: true, CacheDir: dir}
	if err := os.MkdirAll(filepath.Join(dir, "challenges"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "challenges", "token123"), []byte("token123.keyauth-456"), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles := func() []Profile { return []Profile{cfg} }
	h := ChallengeHandler(profiles)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://training.example.com/.well-known/acme-challenge/token123", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "token123.keyauth-456" {
		t.Fatalf("challenge response = %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://training.example.com/.well-known/acme-challenge/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing token = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://training.example.com/login?x=1", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("redirect = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://training.example.com/login?x=1" {
		t.Fatalf("redirect to %q", loc)
	}

	// Path traversal must not escape the challenge directory.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://training.example.com/.well-known/acme-challenge/../../etc/passwd", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("traversal = %d", rec.Code)
	}
}

func TestRequestValidatesBeforeNetwork(t *testing.T) {
	cfg := Profile{Name: "p", Domains: []string{"training.example.com"}, AcceptTOS: false, CacheDir: t.TempDir()}
	if _, err := Request(t.Context(), cfg, nil); err == nil || !strings.Contains(err.Error(), "terms") {
		t.Fatalf("TOS not enforced: %v", err)
	}
	cfg.AcceptTOS = true
	cfg.Domains = nil
	if _, err := Request(t.Context(), cfg, nil); err == nil {
		t.Fatal("empty domain list accepted")
	}
}

// Two profiles, two certificates: SNI must pick the right one.
func TestServerConfigSelectsBySNI(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	writeSelfSigned(t, a, "alpha.example.com")
	writeSelfSigned(t, b, "beta.example.com")
	profiles := []Profile{
		{Name: "alpha", Domains: []string{"alpha.example.com"}, AcceptTOS: true, CacheDir: a},
		{Name: "beta", Domains: []string{"beta.example.com", "www.beta.example.com"}, AcceptTOS: true, CacheDir: b},
	}
	cfg := ServerConfig(func() []Profile { return profiles })

	get := func(host string) string {
		cert, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: host})
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return leaf.DNSNames[0]
	}
	if got := get("alpha.example.com"); got != "alpha.example.com" {
		t.Fatalf("alpha served %q", got)
	}
	if got := get("BETA.example.com"); got != "beta.example.com" {
		t.Fatalf("case-insensitive SNI match failed: %q", got)
	}
	if got := get("www.beta.example.com"); got != "beta.example.com" {
		t.Fatalf("second domain of a profile failed: %q", got)
	}
	if _, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.example.com"}); err == nil {
		t.Fatal("unknown hostname served a certificate")
	} else if !strings.Contains(err.Error(), "no certificate profile covers") {
		t.Fatalf("unhelpful error: %v", err)
	}

	// A profile without a cached certificate fails only for its own hostname.
	profiles = append(profiles, Profile{Name: "gamma", Domains: []string{"gamma.example.com"}, CacheDir: t.TempDir()})
	if _, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "gamma.example.com"}); err == nil {
		t.Fatal("missing certificate served")
	}
	if got := get("alpha.example.com"); got != "alpha.example.com" {
		t.Fatalf("alpha broke after adding a profile: %q", got)
	}
}

// The challenge handler searches every profile's challenge directory.
func TestChallengeHandlerAcrossProfiles(t *testing.T) {
	one, two := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(two, "challenges"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(two, "challenges", "tok2"), []byte("tokenabc.thumbprint-123_xyz"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := ChallengeHandler(func() []Profile {
		return []Profile{{Name: "one", CacheDir: one}, {Name: "two", CacheDir: two}}
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://x/.well-known/acme-challenge/tok2", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "tokenabc.thumbprint-123_xyz" {
		t.Fatalf("challenge from second profile = %d %q", rec.Code, rec.Body.String())
	}
}
