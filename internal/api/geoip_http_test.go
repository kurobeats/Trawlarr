package api

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"

	"github.com/kurobeats/trawlarr/internal/geo"
)

// writeGeoFixture builds a minimal city database for the API tests.
func writeGeoFixture(t *testing.T, path string) {
	t.Helper()
	tree, err := mmdbwriter.New(mmdbwriter.Options{
		DatabaseType: "GeoLite2-City",
		Description:  map[string]string{"en": "API test database"},
		RecordSize:   28,
		IPVersion:    6,
		// 203.0.113.0/24 is documentation space, which the writer reserves by
		// default; the test needs it insertable.
		IncludeReservedNetworks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	value := mmdbtype.Map{
		"city":    mmdbtype.Map{"names": mmdbtype.Map{"en": mmdbtype.String("Lisbon")}},
		"country": mmdbtype.Map{"iso_code": mmdbtype.String("PT"), "names": mmdbtype.Map{"en": mmdbtype.String("Portugal")}},
		"location": mmdbtype.Map{
			"latitude":  mmdbtype.Float64(38.72),
			"longitude": mmdbtype.Float64(-9.14),
		},
	}
	_, ipnet, err := net.ParseCIDR("203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Insert(ipnet, value); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := tree.WriteTo(f); err != nil {
		t.Fatal(err)
	}
}

func geoHarness(t *testing.T) (*harness, *geo.Resolver, string) {
	t.Helper()
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "geoip.mmdb")
	resolver := geo.New(path)
	h.srv.Geo = resolver
	t.Cleanup(func() { resolver.Close() })
	return h, resolver, path
}

func TestGeoIPStatusAndUpload(t *testing.T) {
	h, resolver, path := geoHarness(t)
	c := h.login()

	status := h.expect(c, http.MethodGet, "/api/v1/geoip", nil, http.StatusOK)
	if status["available"] != false {
		t.Fatalf("available before upload: %v", status["available"])
	}

	// Garbage is rejected and nothing is installed.
	body, contentType := multipartBody(t, "file", "junk.mmdb", []byte("not a database"))
	req, _ := http.NewRequest(http.MethodPost, h.admin.URL+"/api/v1/geoip/upload", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("garbage upload = %d", resp.StatusCode)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("garbage was installed")
	}

	// A real database installs.
	src := filepath.Join(t.TempDir(), "fixture.mmdb")
	writeGeoFixture(t, src)
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	body, contentType = multipartBody(t, "file", "dbip-city-lite.mmdb", raw)
	req, _ = http.NewRequest(http.MethodPost, h.admin.URL+"/api/v1/geoip/upload", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload = %d", resp.StatusCode)
	}

	status = h.expect(c, http.MethodGet, "/api/v1/geoip", nil, http.StatusOK)
	if status["available"] != true {
		t.Fatalf("not available after upload: %v", status)
	}
	db := status["database"].(map[string]any)
	if db["database_type"] != "GeoLite2-City" || db["description"] != "API test database" {
		t.Fatalf("database info = %v", db)
	}

	// The lookup preview finds the fixture record and misses private space.
	found := h.expect(c, http.MethodPost, "/api/v1/geoip/lookup", map[string]string{"ip": "203.0.113.42"}, http.StatusOK)
	if found["found"] != true {
		t.Fatalf("lookup = %v", found)
	}
	loc := found["location"].(map[string]any)
	if loc["city"] != "Lisbon" || loc["country_code"] != "PT" {
		t.Fatalf("location = %v", loc)
	}
	missing := h.expect(c, http.MethodPost, "/api/v1/geoip/lookup", map[string]string{"ip": "10.0.0.1"}, http.StatusOK)
	if missing["found"] != false {
		t.Fatalf("private address resolved: %v", missing)
	}
	_ = resolver
}

func TestGeoIPDownload(t *testing.T) {
	h, _, _ := geoHarness(t)
	admin := h.login()

	// A download already running is refused rather than queued twice.
	h.srv.geoDL.mu.Lock()
	h.srv.geoDL.running = true
	h.srv.geoDL.mu.Unlock()
	if status, body := h.do(admin, http.MethodPost, h.admin.URL+"/api/v1/geoip/download", nil); status != http.StatusConflict {
		t.Fatalf("concurrent download = %d %s", status, body)
	}
	h.srv.geoDL.mu.Lock()
	h.srv.geoDL.running = false
	h.srv.geoDL.mu.Unlock()

	// Operators may upload but not trigger a download.
	h.expect(admin, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "op", "password": "operator-password-1", "role": "operator",
	}, http.StatusCreated)
	op := h.client()
	h.do(op, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "op", "password": "operator-password-1"})
	if status, _ := h.do(op, http.MethodPost, h.admin.URL+"/api/v1/geoip/download", nil); status != http.StatusForbidden {
		t.Fatalf("operator download = %d", status)
	}
}

func TestGeoIPDisabled(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	status := h.expect(c, http.MethodGet, "/api/v1/geoip", nil, http.StatusOK)
	if status["available"] != false {
		t.Fatalf("unexpected availability: %v", status)
	}
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/geoip/lookup", map[string]string{"ip": "1.1.1.1"}); status != http.StatusNotImplemented {
		t.Fatalf("lookup without a resolver = %d", status)
	}
	body, contentType := multipartBody(t, "file", "x.mmdb", []byte("x"))
	req, _ := http.NewRequest(http.MethodPost, h.admin.URL+"/api/v1/geoip/upload", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("upload without a resolver = %d", resp.StatusCode)
	}
}
