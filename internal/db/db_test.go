package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestMigrationsAreAppliedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.db")
	conn, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied == 0 {
		t.Fatal("no migrations recorded")
	}
	conn.Close()

	// Reopening must not re-run them or reset data.
	conn, err = Open(path)
	if err != nil {
		t.Fatalf("second open failed: %v", err)
	}
	defer conn.Close()
	var again int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if again != applied {
		t.Fatalf("migrations re-applied: %d then %d", applied, again)
	}
	if _, err := conn.Exec(`INSERT INTO orgs (name, created_at) VALUES ('x', 1)`); err != nil {
		t.Fatalf("schema unusable after reopen: %v", err)
	}
}

func TestWalAndForeignKeysEnabled(t *testing.T) {
	conn := openTest(t)
	var mode string
	if err := conn.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var fk int
	if err := conn.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatal("foreign keys are not enforced")
	}
	// A target pointing at a non-existent group must be refused.
	_, err := conn.Exec(`INSERT INTO targets (group_id, email) VALUES (9999, 'x@example.com')`)
	if err == nil {
		t.Fatal("orphan target accepted")
	}
}

func TestRequiredTablesExist(t *testing.T) {
	conn := openTest(t)
	for _, table := range []string{
		"orgs", "users", "sessions", "audit_log", "target_groups", "targets",
		"attachments", "templates", "pages", "profiles", "campaigns",
		"campaign_targets", "events", "webhooks", "settings", "proxy_sessions",
	} {
		var n int
		err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s is missing", table)
		}
	}
}

func TestOpenCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "trawlarr.db")
	conn, err := Open(path)
	if err != nil {
		t.Fatalf("open with missing parent directory: %v", err)
	}
	conn.Close()
}

func TestOpenRejectsUnusablePath(t *testing.T) {
	// A directory cannot be used as a database file.
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("opening a directory as a database succeeded")
	}
}
