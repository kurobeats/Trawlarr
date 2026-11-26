package geo

import (
	"bytes"
	"compress/gzip"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
)

// writeFixture builds a two-record GeoLite2-shaped database.
func writeFixture(t *testing.T, path string) {
	t.Helper()
	tree, err := mmdbwriter.New(mmdbwriter.Options{
		DatabaseType:            "GeoLite2-City",
		Description:             map[string]string{"en": "Trawlarr test database"},
		RecordSize:              28,
		IPVersion:               6,
		DisableIPv4Aliasing:     false,
		IncludeReservedNetworks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	record := func(city string, lat, lon float64, cc string) mmdbtype.Map {
		return mmdbtype.Map{
			"city": mmdbtype.Map{
				"names": mmdbtype.Map{"en": mmdbtype.String(city)},
			},
			"country": mmdbtype.Map{
				"iso_code": mmdbtype.String(cc),
				"names":    mmdbtype.Map{"en": mmdbtype.String(cc + " name")},
			},
			"location": mmdbtype.Map{
				"latitude":  mmdbtype.Float64(lat),
				"longitude": mmdbtype.Float64(lon),
			},
		}
	}
	for network, value := range map[string]mmdbtype.Map{
		"203.0.113.0/24":  record("London", 51.5074, -0.1278, "GB"),
		"198.51.100.0/24": record("Berlin", 52.52, 13.405, "DE"),
	} {
		_, ipnet, err := net.ParseCIDR(network)
		if err != nil {
			t.Fatal(err)
		}
		if err := tree.Insert(ipnet, value); err != nil {
			t.Fatal(err)
		}
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

func TestLookup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "geoip.mmdb")
	r := New(path)

	if r.Available() {
		t.Fatal("resolver reports a database before one exists")
	}
	if _, ok := r.Lookup("203.0.113.5"); ok {
		t.Fatal("lookup without a database succeeded")
	}

	writeFixture(t, path)
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Available() {
		t.Fatal("resolver did not load the database")
	}

	loc, ok := r.Lookup("203.0.113.5")
	if !ok {
		t.Fatal("known address not found")
	}
	if loc.City != "London" || loc.CountryCode != "GB" || loc.Latitude < 51 || loc.Latitude > 52 {
		t.Fatalf("unexpected location: %+v", loc)
	}

	loc, ok = r.Lookup("198.51.100.7")
	if !ok || loc.City != "Berlin" {
		t.Fatalf("second network = %+v, %v", loc, ok)
	}

	// Private, loopback, unspecified and malformed addresses are not resolved.
	for _, ip := range []string{"10.0.0.1", "192.168.1.1", "127.0.0.1", "::1", "0.0.0.0", "not-an-ip", ""} {
		if _, ok := r.Lookup(ip); ok {
			t.Errorf("%q resolved", ip)
		}
	}
	// Unknown public address.
	if _, ok := r.Lookup("8.8.8.8"); ok {
		t.Error("address outside the database resolved")
	}
}

func TestInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geoip.mmdb")
	r := New(path)
	if info := r.Info(); info.Installed || info.Error != "" {
		t.Fatalf("missing database info = %+v", info)
	}

	writeFixture(t, path)
	info := r.Info()
	if !info.Installed || info.Size == 0 {
		t.Fatalf("info = %+v", info)
	}
	if info.DatabaseType != "GeoLite2-City" || info.Description != "Trawlarr test database" {
		t.Fatalf("metadata = %+v", info)
	}
	if info.Modified.IsZero() {
		t.Fatal("modification time missing")
	}
}

func TestInstallRejectsGarbageAndKeepsTheOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geoip.mmdb")
	r := New(path)
	writeFixture(t, path)
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if err := r.Install(strings.NewReader("this is not an mmdb")); err == nil {
		t.Fatal("garbage accepted")
	}
	if _, ok := r.Lookup("203.0.113.5"); !ok {
		t.Fatal("the working database was destroyed by a failed install")
	}
}

func TestInstallReplacesAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geoip.mmdb")
	r := New(path)
	writeFixture(t, path)
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	fresh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := r.Install(fresh); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup("198.51.100.9"); !ok {
		t.Fatal("lookup failed after reinstall")
	}
}

func TestDownloadInstallsFromGzip(t *testing.T) {
	// Build a gzipped fixture and serve it, standing in for DB-IP.
	src := filepath.Join(t.TempDir(), "fixture.mmdb")
	writeFixture(t, src)
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(compressed.Bytes())
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "geoip.mmdb")
	r := New(path)
	old := downloadURL
	downloadURL = func(time.Time) string { return srv.URL }
	defer func() { downloadURL = old }()

	url, err := r.Download(context.Background())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if url != srv.URL {
		t.Fatalf("url = %q", url)
	}
	if _, ok := r.Lookup("203.0.113.4"); !ok {
		t.Fatal("downloaded database not usable")
	}
}

func TestDBIPURLIsMonthlyAndPredictable(t *testing.T) {
	got := DBIPURL(time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC))
	want := "https://download.db-ip.com/free/dbip-city-lite-2026-11.mmdb.gz"
	if got != want {
		t.Fatalf("DBIPURL = %q, want %q", got, want)
	}
}

func TestLocateAdaptsForTheStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geoip.mmdb")
	writeFixture(t, path)
	r := New(path)
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	lat, lon, city, country, ok := r.Locate("203.0.113.9")
	if !ok || city != "London" || country == "" || lat == 0 || lon == 0 {
		t.Fatalf("Locate = %v %v %q %q %v", lat, lon, city, country, ok)
	}
	if _, _, _, _, ok := r.Locate("10.1.2.3"); ok {
		t.Fatal("private address located")
	}
}
