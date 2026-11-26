package tlsutil

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

// stagingDirectory issues throwaway certificates for testing; browsers do not
// trust them, but it validates the challenge path without burning production
// rate limits.
const stagingDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"

// productionDirectory is the real Let's Encrypt endpoint.
const productionDirectory = acme.LetsEncryptURL

// cacheDirOf is where certificates and the account key live.
func cacheDirOf(cfg Profile) string {
	if cfg.CacheDir != "" {
		return cfg.CacheDir
	}
	return filepath.Join("data", "acme")
}

// challengeDir holds the HTTP-01 tokens while a request is in flight.
func challengeDir(cfg Profile) string {
	return filepath.Join(cacheDirOf(cfg), "challenges")
}

// certPath is the cached certificate for a domain.
func certPath(cfg Profile, domain string) string {
	return filepath.Join(cacheDirOf(cfg), domain+".pem")
}

// Request obtains a certificate for the configured domains using an HTTP-01
// challenge. Issuance is manual: nothing is requested until this is called.
//
// The challenge files are written under the cache directory, so the HTTP
// listener must be serving ChallengeHandler on the challenge address for the
// duration of the call (one to two minutes at most).
func Request(ctx context.Context, cfg Profile, log func(string, ...any)) (*IssueResult, error) {
	domains, err := NormaliseDomains(cfg.Domains)
	if err != nil {
		return nil, err
	}
	if len(domains) == 0 {
		return nil, errors.New("no domains configured")
	}
	if !cfg.AcceptTOS {
		return nil, errors.New("accept the CA terms of service first")
	}
	if err := os.MkdirAll(challengeDir(cfg), 0o750); err != nil {
		return nil, err
	}

	accountKey, err := loadOrCreateAccountKey(cfg)
	if err != nil {
		return nil, err
	}
	client := &acme.Client{Key: accountKey}
	if cfg.Staging {
		client.DirectoryURL = stagingDirectory
	} else {
		client.DirectoryURL = productionDirectory
	}
	if log != nil {
		log("registering ACME account", "email", cfg.Email, "staging", cfg.Staging)
	}
	contact := []string{}
	if strings.TrimSpace(cfg.Email) != "" {
		contact = append(contact, "mailto:"+strings.TrimSpace(cfg.Email))
	}
	if _, err := client.Register(ctx, &acme.Account{Contact: contact}, acme.AcceptTOS); err != nil {
		// An existing account is fine; only a real failure should stop us.
		if !isAccountExists(err) {
			return nil, fmt.Errorf("register: %w", err)
		}
	}

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(domains...))
	if err != nil {
		return nil, fmt.Errorf("authorize: %w", err)
	}

	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return nil, fmt.Errorf("authorization: %w", err)
		}
		if authz.Status == acme.StatusValid {
			continue
		}
		challenge := pickHTTP01(authz)
		if challenge == nil {
			return nil, fmt.Errorf("%s: no http-01 challenge offered", authz.Identifier.Value)
		}
		response, err := client.HTTP01ChallengeResponse(challenge.Token)
		if err != nil {
			return nil, err
		}
		tokenFile := filepath.Join(challengeDir(cfg), challenge.Token)
		// #nosec G306 -- the challenge file holds a public key authorization.
		if err := os.WriteFile(tokenFile, []byte(response), 0o600); err != nil {
			return nil, err
		}
		if log != nil {
			log("answering http-01 challenge", "domain", authz.Identifier.Value)
		}
		if _, err := client.Accept(ctx, challenge); err != nil {
			os.Remove(tokenFile)
			return nil, fmt.Errorf("accept challenge: %w", err)
		}
		if _, err := client.WaitAuthorization(ctx, authz.URI); err != nil {
			os.Remove(tokenFile)
			return nil, fmt.Errorf("%s: %w", authz.Identifier.Value, err)
		}
		os.Remove(tokenFile)
	}

	csr, keyPEM, err := newCSR(domains)
	if err != nil {
		return nil, err
	}
	chain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, fmt.Errorf("finalize: %w", err)
	}

	var buf strings.Builder
	buf.Write(keyPEM)
	for _, der := range chain {
		buf.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	if err := os.WriteFile(certPath(cfg, domains[0]), []byte(buf.String()), 0o600); err != nil {
		return nil, err
	}
	cert, err := parseCached([]byte(buf.String()))
	if err != nil {
		return nil, err
	}
	if log != nil {
		log("certificate issued", "domains", domains, "expires", cert.NotAfter)
	}
	return &IssueResult{
		Domain:   domains[0],
		Issuer:   cert.Issuer.CommonName,
		NotAfter: Time{cert.NotAfter},
		DNSNames: cert.DNSNames,
	}, nil
}

// ServerConfig serves cached certificates and picks one by SNI: the profile
// whose domain list covers the requested hostname wins. It reloads a file when
// its mtime changes, so a certificate requested in the UI takes effect on the
// next handshake without restarting.
//
// It never contacts the CA. A hostname with no cached certificate fails the
// handshake with an error pointing at the Certificates page.
func ServerConfig(lookup func() []Profile) *tls.Config {
	loader := &certLoader{lookup: lookup, loaded: map[string]*cachedCert{}}
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"h2", "http/1.1"},
		GetCertificate: loader.get,
	}
}

type cachedCert struct {
	modTime time.Time
	cert    *tls.Certificate
}

// certLoader caches parsed pairs per cache path and reloads on mtime changes.
type certLoader struct {
	lookup func() []Profile
	mu     sync.Mutex
	loaded map[string]*cachedCert
}

func (l *certLoader) get(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := strings.ToLower(strings.TrimSuffix(hello.ServerName, "."))
	profiles := l.lookup()

	chosen := matchProfile(profiles, name)
	if chosen == nil {
		if name == "" && len(profiles) > 0 {
			chosen = &profiles[0]
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("no certificate profile covers %q; add one under Certificates", hello.ServerName)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loadLocked(certPath(*chosen, firstDomain(*chosen)))
}

// matchProfile returns the profile listing the hostname.
func matchProfile(profiles []Profile, host string) *Profile {
	for i := range profiles {
		domains, err := NormaliseDomains(profiles[i].Domains)
		if err != nil {
			continue
		}
		for _, d := range domains {
			if d == host {
				return &profiles[i]
			}
		}
	}
	return nil
}

func (l *certLoader) loadLocked(path string) (*tls.Certificate, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("no certificate cached yet; request one under Certificates (%s)", path)
	}
	if c, ok := l.loaded[path]; ok && info.ModTime().Equal(c.modTime) {
		return c.cert, nil
	}
	pair, err := tls.LoadX509KeyPair(path, path)
	if err != nil {
		return nil, fmt.Errorf("cached certificate unreadable: %w", err)
	}
	l.loaded[path] = &cachedCert{modTime: info.ModTime(), cert: &pair}
	return &pair, nil
}

func firstDomain(cfg Profile) string {
	domains, err := NormaliseDomains(cfg.Domains)
	if err != nil || len(domains) == 0 {
		return ""
	}
	return domains[0]
}

// ChallengeHandler serves the HTTP-01 token files and redirects everything
// else to HTTPS, so port 80 is only useful for the challenge and redirects.
func ChallengeHandler(lookup func() []Profile) httpHandler {
	return challengeHandler{lookup: lookup}
}

func pickHTTP01(authz *acme.Authorization) *acme.Challenge {
	for _, c := range authz.Challenges {
		if c.Type == "http-01" {
			return c
		}
	}
	return nil
}

// loadOrCreateAccountKey persists the ACME account key so re-requests reuse the
// same registration instead of creating a new account every time.
func loadOrCreateAccountKey(cfg Profile) (crypto.Signer, error) {
	path := filepath.Join(cacheDirOf(cfg), "acme_account.key")
	// #nosec G304 -- path is the cache directory plus a fixed file name.
	if raw, err := os.ReadFile(path); err == nil {
		if block, _ := pem.Decode(raw); block != nil {
			if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
				return key, nil
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheDirOf(cfg), 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// newCSR builds the certificate request and its key, both PEM encoded.
func newCSR(domains []string) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	template := &x509.CertificateRequest{DNSNames: domains, Subject: pkix.Name{CommonName: domains[0]}}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

func isAccountExists(err error) bool {
	var acmeErr *acme.Error
	if errors.As(err, &acmeErr) {
		return acmeErr.StatusCode == 409
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already") || strings.Contains(msg, "conflict")
}
