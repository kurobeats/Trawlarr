// Package tlsutil issues and serves Let's Encrypt certificates on demand.
//
// Issuance is manual by design: nothing is requested until the operator presses
// "Request certificate" in Settings, and renewal is the same call. The listener
// then serves whatever is cached, so an expired certificate stays expired until
// someone renews it.
package tlsutil

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kurobeats/trawlarr/internal/store"
)

// Profile is one certificate profile: the domains it covers and where the
// issued material lives. It mirrors the stored profile without the ORM types.
type Profile struct {
	Name      string
	Domains   []string
	Email     string
	Staging   bool
	AcceptTOS bool
	CacheDir  string
	HTTPAddr  string
}

// FromCertificate adapts a stored profile.
func FromCertificate(c *store.Certificate) Profile {
	return Profile{
		Name: c.Name, Domains: c.Domains, Email: c.Email, Staging: c.Staging,
		AcceptTOS: c.AcceptTOS, CacheDir: c.CacheDir, HTTPAddr: c.HTTPAddr,
	}
}

// IssueResult is the outcome of a successful request.
type IssueResult struct {
	Domain   string   `json:"domain"`
	Issuer   string   `json:"issuer"`
	NotAfter Time     `json:"not_after"`
	DNSNames []string `json:"dns_names"`
}

// Time marshals as RFC 3339, or null when unset.
type Time struct{ time.Time }

// MarshalJSON emits RFC 3339 or null.
func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return []byte(`"` + t.UTC().Format(time.RFC3339) + `"`), nil
}

// Status is what the UI shows about the current certificate.
type Status struct {
	Staging      bool          `json:"staging"`
	CacheDir     string        `json:"cache_dir"`
	HTTPAddr     string        `json:"http_addr"`
	Domains      []string      `json:"domains"`
	Certificates []IssueResult `json:"certificates"`
	// RenewsAt is 30 days before the earliest expiry: when Let's Encrypt
	// recommends renewing.
	RenewsAt Time   `json:"renews_at"`
	Error    string `json:"error,omitempty"`
}

// ReadStatus inspects the cache without contacting the CA.
func ReadStatus(cfg Profile) Status {
	st := Status{
		Staging:      cfg.Staging,
		CacheDir:     cacheDirOf(cfg),
		HTTPAddr:     ChallengeAddr(cfg),
		Domains:      []string{},
		Certificates: []IssueResult{},
	}
	domains, err := NormaliseDomains(cfg.Domains)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Domains = domains

	entries, err := os.ReadDir(st.CacheDir)
	if err != nil {
		if !os.IsNotExist(err) {
			st.Error = err.Error()
		}
		return st
	}
	var earliest time.Time
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".key") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(st.CacheDir, entry.Name()))
		if err != nil {
			continue
		}
		cert, err := parseCached(raw)
		if err != nil {
			continue
		}
		st.Certificates = append(st.Certificates, IssueResult{
			Domain:   strings.TrimSuffix(entry.Name(), ".pem"),
			Issuer:   cert.Issuer.CommonName,
			NotAfter: Time{cert.NotAfter},
			DNSNames: cert.DNSNames,
		})
		if earliest.IsZero() || cert.NotAfter.Before(earliest) {
			earliest = cert.NotAfter
		}
	}
	sort.Slice(st.Certificates, func(i, j int) bool { return st.Certificates[i].Domain < st.Certificates[j].Domain })
	if !earliest.IsZero() {
		st.RenewsAt = Time{earliest.Add(-30 * 24 * time.Hour)}
	}
	return st
}

// ChallengeAddr is where the HTTP-01 challenge is answered.
func ChallengeAddr(cfg Profile) string {
	if strings.TrimSpace(cfg.HTTPAddr) == "" {
		return ":80"
	}
	return cfg.HTTPAddr
}

// NormaliseDomains lowercases, trims and de-duplicates a domain list. It
// accepts commas or whitespace as separators.
func NormaliseDomains(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range in {
		parts := strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\n' || r == '\t'
		})
		for _, part := range parts {
			d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(part), "."))
			if d == "" {
				continue
			}
			if strings.Contains(d, "://") || strings.ContainsAny(d, "/ :") {
				return nil, fmt.Errorf("%q is not a hostname (no scheme, port or path)", part)
			}
			if !strings.Contains(d, ".") && d != "localhost" {
				return nil, fmt.Errorf("%q is not a fully qualified domain name", part)
			}
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// parseCached finds the first certificate in a cache file.
func parseCached(raw []byte) (*x509.Certificate, error) {
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			return nil, errors.New("no certificate in cache entry")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}
