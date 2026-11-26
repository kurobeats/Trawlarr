package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCertificateCRUD(t *testing.T) {
	h := newHarness(t)
	c := h.login()

	created := h.expect(c, http.MethodPost, "/api/v1/certificates", map[string]any{
		"name": "Training", "domains": []string{"Training.Example.com", "www.training.example.com"},
		"email": "soc@example.com", "accept_tos": true, "staging": true,
		"cache_dir": "/tmp/certs-training", "http_addr": ":8080",
	}, http.StatusCreated)
	id := int64(created["id"].(float64))
	domains := created["domains"].([]any)
	if len(domains) != 2 || domains[0] != "training.example.com" {
		t.Fatalf("domains = %v", domains)
	}
	if created["status"].(map[string]any)["cache_dir"] != "/tmp/certs-training" {
		t.Fatalf("status missing: %v", created["status"])
	}

	raw := h.expectRaw(c, http.MethodGet, "/api/v1/certificates", nil, http.StatusOK)
	if !strings.Contains(string(raw), "Training") || !strings.Contains(string(raw), `"usage":0`) {
		t.Fatalf("list = %s", raw)
	}

	updated := h.expect(c, http.MethodPut, "/api/v1/certificates/"+itoa(id), map[string]any{
		"name": "Training v2", "domains": []string{"training.example.com"}, "accept_tos": true,
	}, http.StatusOK)
	if updated["name"] != "Training v2" {
		t.Fatalf("update not applied: %v", updated)
	}
	h.expect(c, http.MethodDelete, "/api/v1/certificates/"+itoa(id), nil, http.StatusOK)
	h.expect(c, http.MethodGet, "/api/v1/certificates/"+itoa(id), nil, http.StatusNotFound)
}

func TestCertificateValidation(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	for _, body := range []map[string]any{
		{"name": "", "domains": []string{"example.com"}},
		{"name": "x", "domains": []string{}},
		{"name": "x", "domains": []string{"https://example.com"}},
		{"name": "x", "domains": []string{"example.com:443"}},
		{"name": "x", "domains": []string{"localhost-x"}},
		{"name": "x", "domains": []string{"example.com"}, "email": "nope"},
		{"name": "x", "domains": []string{"example.com"}, "http_addr": "80"},
	} {
		if status, resp := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/certificates", body); status != http.StatusBadRequest {
			t.Errorf("%v accepted (%d): %s", body, status, resp)
		}
	}
}

func TestCertificateStatusReadsCache(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	dir := t.TempDir()
	created := h.expect(c, http.MethodPost, "/api/v1/certificates", map[string]any{
		"name": "Training", "domains": []string{"training.example.com"}, "cache_dir": dir, "accept_tos": true,
	}, http.StatusCreated)
	id := itoa(int64(created["id"].(float64)))

	got := h.expect(c, http.MethodGet, "/api/v1/certificates/"+id, nil, http.StatusOK)
	if certs := got["status"].(map[string]any)["certificates"].([]any); len(certs) != 0 {
		t.Fatalf("unexpected certificates: %v", certs)
	}

	writeTestCert(t, dir, "training.example.com")
	got = h.expect(c, http.MethodGet, "/api/v1/certificates/"+id, nil, http.StatusOK)
	certs := got["status"].(map[string]any)["certificates"].([]any)
	if len(certs) != 1 {
		t.Fatalf("certificates = %v", certs)
	}
	entry := certs[0].(map[string]any)
	if entry["issuer"] != "Test CA" || entry["not_after"] == nil {
		t.Fatalf("entry = %v", entry)
	}
	if got["status"].(map[string]any)["renews_at"] == nil {
		t.Fatal("renewal time missing")
	}
}

func TestCertificateIssuePreflight(t *testing.T) {
	h := newHarness(t)
	admin := h.login()
	created := h.expect(admin, http.MethodPost, "/api/v1/certificates", map[string]any{
		"name": "NoTos", "domains": []string{"training.example.com"}, "cache_dir": t.TempDir(),
	}, http.StatusCreated)
	id := itoa(int64(created["id"].(float64)))

	if status, body := h.do(admin, http.MethodPost, h.admin.URL+"/api/v1/certificates/"+id+"/request", nil); status != http.StatusBadRequest ||
		!strings.Contains(string(body), "terms of service") {
		t.Fatalf("TOS not enforced: %d %s", status, body)
	}
	if status, _ := h.do(admin, http.MethodPost, h.admin.URL+"/api/v1/certificates/"+id+"/explode", nil); status != http.StatusBadRequest {
		t.Fatalf("bad action = %d", status)
	}

	// Operators cannot trigger issuance.
	h.expect(admin, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "op", "password": "operator-password-1", "role": "operator",
	}, http.StatusCreated)
	op := h.client()
	h.do(op, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "op", "password": "operator-password-1"})
	if status, _ := h.do(op, http.MethodPost, h.admin.URL+"/api/v1/certificates/"+id+"/renew", nil); status != http.StatusForbidden {
		t.Fatalf("operator renew = %d", status)
	}

	// A second concurrent request is refused rather than racing the CA.
	h.expect(admin, http.MethodPut, "/api/v1/certificates/"+id, map[string]any{
		"name": "Ready", "domains": []string{"training.example.com"}, "accept_tos": true,
	}, http.StatusOK)
	h.srv.tlsMu.Lock()
	status, body := h.do(admin, http.MethodPost, h.admin.URL+"/api/v1/certificates/"+id+"/request", nil)
	h.srv.tlsMu.Unlock()
	if status != http.StatusConflict || !strings.Contains(string(body), "already running") {
		t.Fatalf("concurrent request = %d %s", status, body)
	}
}

func TestCertificateDeletionBlockedByCampaign(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	created := h.expect(c, http.MethodPost, "/api/v1/certificates", map[string]any{
		"name": "Training", "domains": []string{"phish.test"}, "accept_tos": true, "cache_dir": t.TempDir(),
	}, http.StatusCreated)
	id := int64(created["id"].(float64))

	h.expect(c, http.MethodPost, "/api/v1/campaigns", map[string]any{
		"name": "Q4", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
		"url": "https://phish.test", "certificate_id": id,
	}, http.StatusCreated)

	if status, body := h.do(c, http.MethodDelete, h.admin.URL+"/api/v1/certificates/"+itoa(id), nil); status != http.StatusConflict {
		t.Fatalf("deletion = %d %s", status, body)
	}

	// Usage is reported so the UI can explain why.
	got := h.expect(c, http.MethodGet, "/api/v1/certificates/"+itoa(id), nil, http.StatusOK)
	if got["usage"].(float64) != 1 {
		t.Fatalf("usage = %v", got["usage"])
	}
}

func TestCampaignCertificateMustCoverTrackingHost(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	created := h.expect(c, http.MethodPost, "/api/v1/certificates", map[string]any{
		"name": "Only training", "domains": []string{"training.example.com"}, "accept_tos": true,
		"cache_dir": t.TempDir(),
	}, http.StatusCreated)
	id := int64(created["id"].(float64))

	base := map[string]any{
		"name": "Q4", "template_id": 1, "page_id": 1, "profile_id": 1, "group_ids": []int{1},
	}
	// Not covered.
	body := map[string]any{"url": "https://other.example.com"}
	for k, v := range base {
		body[k] = v
	}
	body["certificate_id"] = id
	if status, resp := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/campaigns", body); status != http.StatusBadRequest {
		t.Fatalf("mismatched certificate accepted (%d): %s", status, resp)
	}
	// Covered.
	body["url"] = "https://training.example.com"
	h.expect(c, http.MethodPost, "/api/v1/campaigns", body, http.StatusCreated)
	// Unknown profile.
	body["certificate_id"] = 999
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/campaigns", body); status != http.StatusBadRequest {
		t.Fatalf("unknown certificate accepted: %d", status)
	}
}

// writeTestCert plants a self-signed certificate in the ACME cache layout.
func writeTestCert(t *testing.T, dir, domain string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "Test CA"},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
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
	if err := os.WriteFile(filepath.Join(dir, domain+".pem"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}
