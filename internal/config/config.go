// Package config loads and validates Trawlarr's runtime configuration.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Server describes one HTTP listener.
type Server struct {
	ListenURL string `json:"listen_url"`
	// PublicURL is the address recipients see in links. Set it whenever
	// ListenURL is a wildcard or sits behind a proxy.
	PublicURL string `json:"public_url"`
	UseTLS    bool   `json:"use_tls"`
	CertPath  string `json:"cert_path"`
	KeyPath   string `json:"key_path"`
}

// IMAP configures the mailbox recipients can report simulated phish to.
type IMAP struct {
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	Folder          string `json:"folder"`
	UseTLS          bool   `json:"use_tls"`
	IgnoreCert      bool   `json:"ignore_cert"`
	DeleteReported  bool   `json:"delete_reported"`
	IntervalMinutes int    `json:"interval_minutes"`
}

// Config is the on-disk configuration file.
type Config struct {
	AdminServer  Server   `json:"admin_server"`
	PhishServer  Server   `json:"phish_server"`
	DBPath       string   `json:"db_path"`
	DataDir      string   `json:"data_dir"`
	EncryptionKey string  `json:"encryption_key"`
	LogLevel     string   `json:"log_level"`
	TrustedOrigins []string `json:"trusted_origins"`
	TrustProxy   bool     `json:"trust_proxy"`
	SecureCookies bool    `json:"secure_cookies"`
	SessionTTLHours int   `json:"session_ttl_hours"`
	// Proxy campaigns relay a live site to capture session cookies. Off by
	// default; ProxyAllowlist must name every upstream host you may target.
	ProxyEnabled   bool     `json:"proxy_enabled"`
	ProxyAllowlist []string `json:"proxy_allowlist"`
	IMAP IMAP `json:"imap"`
	// SeedAdminPassword, when set, is used for the initial admin account on a
	// fresh database instead of a generated one.
	SeedAdminPassword string `json:"-"`
	path string
}

// Default returns a config suitable for a first run.
func Default() *Config {
	return &Config{
		AdminServer:     Server{ListenURL: "127.0.0.1:3333"},
		PhishServer:     Server{ListenURL: "0.0.0.0:8080"},
		DBPath:          "trawlarr.db",
		DataDir:         "data",
		LogLevel:        "info",
		TrustedOrigins:  []string{},
		SessionTTLHours: 12,
		ProxyEnabled:    false,
		ProxyAllowlist:  []string{},
	}
}

// Load reads path, creating it with defaults when absent.
func Load(path string) (*Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	case os.IsNotExist(err):
	default:
		return nil, err
	}
	c.path = path
	if c.SeedAdminPassword == "" {
		c.SeedAdminPassword = os.Getenv("TRAWLARR_ADMIN_PASSWORD")
	}
	if c.EncryptionKey == "" {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		c.EncryptionKey = base64.StdEncoding.EncodeToString(key)
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	if err := c.Save(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) normalize() error {
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.SessionTTLHours <= 0 {
		c.SessionTTLHours = 12
	}
	if c.DBPath == "" {
		c.DBPath = "trawlarr.db"
	}
	if c.DataDir == "" {
		c.DataDir = filepath.Dir(c.DBPath)
	}
	if c.EncryptionKey == "" {
		return fmt.Errorf("encryption_key is required")
	}
	raw, err := base64.StdEncoding.DecodeString(c.EncryptionKey)
	if err != nil || len(raw) != 32 {
		return fmt.Errorf("encryption_key must be base64 of 32 bytes")
	}
	for _, s := range []*Server{&c.AdminServer, &c.PhishServer} {
		if s.ListenURL == "" {
			continue
		}
		raw := s.ListenURL
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw // bare host:port is the common case
		}
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("listen_url %q: %w", s.ListenURL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("listen_url %q: unsupported scheme", s.ListenURL)
		}
		if u.Host == "" {
			return fmt.Errorf("listen_url %q: missing host", s.ListenURL)
		}
	}
	return nil
}

// Addr returns just the host:port part of a listen URL.
func (s Server) Addr() string {
	raw := s.ListenURL
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	return strings.TrimSuffix(raw, "/")
}

// BaseURL returns the scheme://host[:port] used to build tracking links.
func (s Server) BaseURL() string {
	if s.PublicURL != "" {
		return strings.TrimSuffix(s.PublicURL, "/")
	}
	scheme := "http"
	if s.UseTLS {
		scheme = "https"
	}
	return scheme + "://" + s.Addr()
}

// Save writes the config back to disk with restrictive permissions.
func (c *Config) Save() error {
	if c.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// ProxyHostAllowed reports whether an upstream host may be relayed.
func (c *Config) ProxyHostAllowed(host string) bool {
	if !c.ProxyEnabled {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, allowed := range c.ProxyAllowlist {
		allowed = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(allowed, "*.")))
		if allowed == "" {
			continue
		}
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

// SameListener reports whether admin and phish traffic share an address.
func (c *Config) SameListener() bool {
	return c.AdminServer.Addr() == c.PhishServer.Addr()
}
