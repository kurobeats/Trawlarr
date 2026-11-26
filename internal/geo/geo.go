// Package geo annotates recipient IPs with a location from a local MaxMind
// format database.
//
// Nothing is looked up over the network: the operator installs a database once
// (MaxMind GeoLite2-City, or DB-IP Lite which needs no account and is CC BY
// 4.0), and every lookup happens on this machine. If no database is installed
// the feature is simply off.
package geo

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"

	"github.com/kurobeats/trawlarr/internal/store"
)

// Location is the part of a database record worth storing.
type Location struct {
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	City        string  `json:"city"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
}

type record struct {
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
		Timezone  string  `maxminddb:"timezone"`
	} `maxminddb:"location"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
}

// Resolver answers lookups from a database file, reloading it when replaced.
type Resolver struct {
	path string

	mu      sync.RWMutex
	db      *maxminddb.Reader
	modTime time.Time
	loaded  bool
}

// New returns a resolver for the database at path. A missing file is not an
// error: the resolver simply reports "no location" until one is installed.
func New(path string) *Resolver {
	return &Resolver{path: path}
}

// Path is where the database is stored.
func (r *Resolver) Path() string { return r.path }

// Available reports whether a database is installed and readable.
func (r *Resolver) Available() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loaded
}

// Lookup resolves an IP address. The second return value is false when the
// address is unknown, private, or no database is installed.
func (r *Resolver) Lookup(ip string) (Location, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil || !addr.IsValid() || addr.IsPrivate() || addr.IsLoopback() || addr.IsUnspecified() {
		return Location{}, false
	}
	db, err := r.reader()
	if err != nil || db == nil {
		return Location{}, false
	}
	var rec record
	res := db.Lookup(addr)
	if err := res.Decode(&rec); err != nil {
		return Location{}, false
	}
	if rec.Location.Latitude == 0 && rec.Location.Longitude == 0 {
		return Location{}, false
	}
	return Location{
		Latitude:    rec.Location.Latitude,
		Longitude:   rec.Location.Longitude,
		City:        rec.City.Names["en"],
		Country:     rec.Country.Names["en"],
		CountryCode: rec.Country.ISOCode,
	}, true
}

// reader returns the open database, reloading when the file changes.
func (r *Resolver) reader() (*maxminddb.Reader, error) {
	r.mu.RLock()
	db, modTime, loaded := r.db, r.modTime, r.loaded
	r.mu.RUnlock()

	info, err := os.Stat(r.path)
	if err != nil {
		return nil, err
	}
	if loaded && db != nil && info.ModTime().Equal(modTime) {
		return db, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded && r.db != nil && info.ModTime().Equal(r.modTime) {
		return r.db, nil
	}
	fresh, err := maxminddb.Open(r.path)
	if err != nil {
		return nil, err
	}
	if r.db != nil {
		_ = r.db.Close()
	}
	r.db = fresh
	r.modTime = info.ModTime()
	r.loaded = true
	return fresh, nil
}

// Load opens the database eagerly, so a broken file is reported at startup.
func (r *Resolver) Load() error {
	_, err := r.reader()
	return err
}

// Close releases the database.
func (r *Resolver) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		_ = r.db.Close()
		r.db = nil
		r.loaded = false
	}
}

// DBInfo describes the installed database.
type DBInfo struct {
	Installed    bool       `json:"installed"`
	Path         string     `json:"path"`
	Size         int64      `json:"size"`
	Modified     store.Time `json:"modified"`
	DatabaseType string     `json:"database_type,omitempty"`
	Description  string     `json:"description,omitempty"`
	Error        string     `json:"error,omitempty"`
}

// Info reports what is installed without keeping the file open.
func (r *Resolver) Info() DBInfo {
	info := DBInfo{Path: r.path}
	stat, err := os.Stat(r.path)
	if err != nil {
		if !os.IsNotExist(err) {
			info.Error = err.Error()
		}
		return info
	}
	info.Size = stat.Size()
	info.Modified = store.TimeOf(stat.ModTime())
	db, err := maxminddb.Open(r.path)
	if err != nil {
		info.Error = "file is not a readable MaxMind database: " + err.Error()
		return info
	}
	defer db.Close()
	info.Installed = true
	info.DatabaseType = db.Metadata.DatabaseType
	for _, lang := range []string{"en", ""} {
		if d := db.Metadata.Description[lang]; d != "" {
			info.Description = d
			break
		}
	}
	return info
}

// Install writes a database atomically and validates it first.
func (r *Resolver) Install(rd io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), "geoip-*.mmdb")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, io.LimitReader(rd, 512<<20)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Validate before replacing a working database.
	db, err := maxminddb.Open(tmpName)
	if err != nil {
		return errors.New("the file is not a MaxMind database: " + err.Error())
	}
	db.Close()
	if err := os.Rename(tmpName, r.path); err != nil {
		return err
	}
	r.mu.Lock()
	r.modTime = time.Time{}
	r.mu.Unlock()
	return r.Load()
}

// downloadURL is a seam for tests; it points at DBIPURL in production.
var downloadURL = DBIPURL

// DBIPURL is the DB-IP Lite city database for a month. It needs no account and
// is licensed CC BY 4.0, which makes it the default downloadable option.
func DBIPURL(month time.Time) string {
	return fmt.Sprintf("https://download.db-ip.com/free/dbip-city-lite-%s.mmdb.gz", month.UTC().Format("2006-01"))
}

// Download fetches and installs a gzipped database, trying this month and then
// the previous one, since the current month is published mid-month.
func (r *Resolver) Download(ctx context.Context) (string, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	var lastErr error
	for _, monthsBack := range []int{0, 1, 2} {
		url := downloadURL(time.Now().AddDate(0, -monthsBack, 0))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s: %s", url, resp.Status)
			continue
		}
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			resp.Body.Close()
			lastErr = err
			continue
		}
		err = r.Install(gz)
		gz.Close()
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return url, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no database available")
	}
	return "", lastErr
}

// Locate adapts the resolver to the store's lookup hook.
func (r *Resolver) Locate(ip string) (lat, lon float64, city, country string, ok bool) {
	loc, found := r.Lookup(ip)
	if !found {
		return 0, 0, "", "", false
	}
	return loc.Latitude, loc.Longitude, loc.City, loc.Country, true
}
