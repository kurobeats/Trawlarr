package api

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFSComplete(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	base := t.TempDir()
	for _, name := range []string{"data", "database", "Downloads"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "data", "trawlarr.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	get := func(typed string) map[string]any {
		return h.expect(c, http.MethodGet, "/api/v1/fs/complete?path="+url.QueryEscape(typed), nil, http.StatusOK)
	}

	// Empty input previews the usual starting points.
	roots := get("")["roots"].([]any)
	if len(roots) == 0 {
		t.Fatal("no starting points returned")
	}

	// Prefix matching, exact before case-insensitive, directories only.
	names := func(typed string) []string {
		got := get(typed)
		matches := got["matches"].([]any)
		out := []string{}
		for _, m := range matches {
			if !strings.HasSuffix(m.(string), string(filepath.Separator)) {
				t.Fatalf("match is not a directory path: %v", m)
			}
			out = append(out, filepath.Base(strings.TrimSuffix(m.(string), string(filepath.Separator))))
		}
		return out
	}

	da := names(filepath.Join(base, "da"))
	if strings.Join(da, ",") != "data,database" {
		t.Fatalf("da matches = %v", da)
	}
	// "D" matches Downloads exactly and the lower-case entries by fold; exact
	// matches come first.
	dd := names(filepath.Join(base, "D"))
	if len(dd) != 3 || dd[0] != "Downloads" {
		t.Fatalf("D matches = %v", dd)
	}
	if strings.Join(dd, ",") == "" || strings.Contains(strings.Join(dd, ","), "trawlarr.db") {
		t.Fatalf("files offered as directories: %v", dd)
	}

	// A directory that does not exist reports why instead of failing.
	missing := get(filepath.Join(base, "nope") + "/")
	if missing["error"] == nil {
		t.Fatalf("missing directory = %v", missing)
	}
}

func TestFSStat(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.db")
	if err := os.WriteFile(existing, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	stat := func(path string) map[string]any {
		return h.expect(c, http.MethodGet, "/api/v1/fs/stat?path="+url.QueryEscape(path), nil, http.StatusOK)
	}

	// Existing file: size and modification time, no complaint.
	got := stat(existing)
	if got["exists"] != true || got["size"].(float64) != 10 {
		t.Fatalf("existing = %v", got)
	}
	if _, bad := got["error"]; bad {
		t.Fatalf("unexpected error for a valid file: %v", got["error"])
	}
	if got["parent_writable"] != true {
		t.Fatalf("parent should be writable: %v", got)
	}

	// New file in an existing directory: allowed, flagged as not existing.
	got = stat(filepath.Join(dir, "new.db"))
	if got["exists"] != false || got["parent_writable"] != true || got["error"] != nil {
		t.Fatalf("new file = %v", got)
	}
	if got["resolved"] != filepath.Join(dir, "new.db") {
		t.Fatalf("resolved = %v", got["resolved"])
	}

	// A directory is not a database file.
	got = stat(dir)
	if got["is_dir"] != true || got["error"] == nil {
		t.Fatalf("directory = %v", got)
	}

	// A missing parent is reported.
	got = stat(filepath.Join(dir, "does", "not", "exist", "x.db"))
	if got["parent_exists"] != false || got["error"] == nil {
		t.Fatalf("missing parent = %v", got)
	}

	// Relative paths resolve against the working directory.
	rel := stat("relative.db")
	if rel["resolved"] != filepath.Join(mustGetwd(t), "relative.db") {
		t.Fatalf("relative resolution = %v", rel["resolved"])
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFSEndpointsRequireAdmin(t *testing.T) {
	h := newHarness(t)
	admin := h.login()
	h.expect(admin, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "op", "password": "operator-password-1", "role": "operator",
	}, http.StatusCreated)
	op := h.client()
	h.do(op, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "op", "password": "operator-password-1"})
	for _, path := range []string{"/api/v1/fs/complete?path=/tmp", "/api/v1/fs/stat?path=/tmp"} {
		if status, _ := h.do(op, http.MethodGet, h.admin.URL+path, nil); status != http.StatusForbidden {
			t.Errorf("%s as operator = %d", path, status)
		}
	}
}
