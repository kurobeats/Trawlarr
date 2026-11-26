package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCreatesFileWithKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EncryptionKey == "" {
		t.Fatal("encryption key not generated")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("written config is not valid JSON: %v", err)
	}
	if onDisk["encryption_key"] != cfg.EncryptionKey {
		t.Fatal("key not persisted")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions are %o, want 600", info.Mode().Perm())
	}
}

func TestLoadIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	first, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.EncryptionKey != second.EncryptionKey {
		t.Fatal("reloading generated a new key; existing secrets would be lost")
	}
}

func TestInvalidFilesAreRejected(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"broken.json":     `{not json`,
		"badkey.json":     `{"encryption_key":"c2hvcnQ="}`,
		"badscheme.json":  `{"encryption_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 32)) + `","phish_server":{"listen_url":"ftp://example.com:21"}}`,
		"emptyhost.json":  `{"encryption_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 32)) + `","admin_server":{"listen_url":"http://"}}`,
	}
	for name, body := range cases {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("%s: invalid config accepted", name)
		}
	}
}

func TestAddressHelpers(t *testing.T) {
	cases := []struct {
		server Server
		addr   string
		base   string
	}{
		{Server{ListenURL: "127.0.0.1:3333"}, "127.0.0.1:3333", "http://127.0.0.1:3333"},
		{Server{ListenURL: "http://0.0.0.0:8080/"}, "0.0.0.0:8080", "http://0.0.0.0:8080"},
		{Server{ListenURL: "0.0.0.0:80", UseTLS: true}, "0.0.0.0:80", "https://0.0.0.0:80"},
		{Server{ListenURL: "0.0.0.0:8080", PublicURL: "https://training.example.com/"}, "0.0.0.0:8080", "https://training.example.com"},
	}
	for _, c := range cases {
		if got := c.server.Addr(); got != c.addr {
			t.Errorf("Addr(%q) = %q, want %q", c.server.ListenURL, got, c.addr)
		}
		if got := c.server.BaseURL(); got != c.base {
			t.Errorf("BaseURL(%q) = %q, want %q", c.server.ListenURL, got, c.base)
		}
	}
}

func TestSameListener(t *testing.T) {
	cfg := Default()
	cfg.AdminServer.ListenURL = "127.0.0.1:9000"
	cfg.PhishServer.ListenURL = "http://127.0.0.1:9000/"
	if !cfg.SameListener() {
		t.Fatal("equivalent listen URLs not detected")
	}
	cfg.PhishServer.ListenURL = "127.0.0.1:9001"
	if cfg.SameListener() {
		t.Fatal("different ports reported as the same listener")
	}
}

func TestProxyHostAllowed(t *testing.T) {
	cfg := Default()
	if cfg.ProxyHostAllowed("login.example.com") {
		t.Fatal("proxy disabled but host allowed")
	}
	cfg.ProxyEnabled = true
	if cfg.ProxyHostAllowed("login.example.com") {
		t.Fatal("empty allowlist allowed a host")
	}
	cfg.ProxyAllowlist = []string{"example.com", "*.corp.internal"}
	allowed := []string{"example.com", "login.example.com", "LOGIN.EXAMPLE.COM", "a.corp.internal"}
	for _, host := range allowed {
		if !cfg.ProxyHostAllowed(host) {
			t.Errorf("%q rejected", host)
		}
	}
	denied := []string{"notexample.com", "example.com.evil.net", "evil.net", ""}
	for _, host := range denied {
		if cfg.ProxyHostAllowed(host) {
			t.Errorf("%q allowed", host)
		}
	}
}

func TestSeedAdminPasswordFromEnv(t *testing.T) {
	t.Setenv("TRAWLARR_ADMIN_PASSWORD", "from-the-environment")
	cfg, err := Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SeedAdminPassword != "from-the-environment" {
		t.Fatalf("env password not picked up: %q", cfg.SeedAdminPassword)
	}
}

func TestNormalizeDefaults(t *testing.T) {
	c := &Config{EncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32))}
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	if c.LogLevel != "info" || c.SessionTTLHours != 12 || c.DBPath == "" || c.DataDir == "" {
		t.Fatalf("defaults not applied: %+v", c)
	}
	c.LogLevel = strings.ToUpper("DEBUG")
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
}
