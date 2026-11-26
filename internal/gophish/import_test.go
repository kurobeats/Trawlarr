package gophish

import (
	"context"
	"database/sql"
	"encoding/base64"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/kurobeats/trawlarr/internal/db"
	"github.com/kurobeats/trawlarr/internal/secret"
	"github.com/kurobeats/trawlarr/internal/store"
)

// buildLegacy creates a database shaped like gophish 0.12 so the importer is
// tested against the real column names.
func buildLegacy(t *testing.T, path string) {
	t.Helper()
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	schema := []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, hash TEXT, api_key TEXT, role_id INTEGER, account_locked BOOLEAN)`,
		`CREATE TABLE roles (id INTEGER PRIMARY KEY, slug TEXT)`,
		`CREATE TABLE targets (id INTEGER PRIMARY KEY, first_name TEXT, last_name TEXT, email TEXT, position TEXT)`,
		`CREATE TABLE "groups" (id INTEGER PRIMARY KEY, user_id INTEGER, name TEXT, modified_date DATETIME)`,
		`CREATE TABLE group_targets (group_id INTEGER, target_id INTEGER)`,
		`CREATE TABLE templates (id INTEGER PRIMARY KEY, user_id INTEGER, name TEXT, subject TEXT, text TEXT, html TEXT, modified_date DATETIME)`,
		`CREATE TABLE attachments (id INTEGER PRIMARY KEY, template_id INTEGER, content TEXT, type TEXT, name TEXT)`,
		`CREATE TABLE pages (id INTEGER PRIMARY KEY, user_id INTEGER, name TEXT, html TEXT, capture_credentials BOOLEAN, capture_passwords BOOLEAN, redirect_url TEXT, modified_date DATETIME)`,
		`CREATE TABLE smtp (id INTEGER PRIMARY KEY, user_id INTEGER, interface_type TEXT, name TEXT, host TEXT, username TEXT, password TEXT, from_address TEXT, ignore_cert_errors BOOLEAN, modified_date DATETIME)`,
		`CREATE TABLE headers (id INTEGER PRIMARY KEY, key TEXT, value TEXT, smtp_id INTEGER)`,
		`CREATE TABLE campaigns (id INTEGER PRIMARY KEY, user_id INTEGER, name TEXT, created_date DATETIME, completed_date DATETIME, launch_date DATETIME, send_by_date DATETIME, template_id INTEGER, page_id INTEGER, smtp_id INTEGER, status TEXT, url TEXT)`,
		`CREATE TABLE campaign_groups (campaign_id INTEGER, group_id INTEGER)`,
		`CREATE TABLE results (id INTEGER PRIMARY KEY, campaign_id INTEGER, user_id INTEGER, r_id TEXT, email TEXT, first_name TEXT, last_name TEXT, position TEXT, status TEXT, ip TEXT, reported BOOLEAN, send_date DATETIME, modified_date DATETIME)`,
		`CREATE TABLE events (id INTEGER PRIMARY KEY, campaign_id INTEGER, email TEXT, time DATETIME, message TEXT, details BLOB)`,
		`CREATE TABLE webhooks (id INTEGER PRIMARY KEY, name TEXT, url TEXT, secret TEXT, is_active BOOLEAN)`,
	}
	for _, q := range schema {
		if _, err := conn.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	attachment := base64.StdEncoding.EncodeToString([]byte("invoice contents"))
	stmts := []string{
		`INSERT INTO roles VALUES (1, 'admin'), (2, 'user')`,
		`INSERT INTO users VALUES (1, 'admin', '$2a$10$abcdefghijklmnopqrstuv', 'legacy-api-key', 1, 0)`,
		`INSERT INTO users VALUES (2, 'analyst', '$2a$10$abcdefghijklmnopqrstuv', 'key2', 2, 0)`,
		`INSERT INTO targets VALUES (1, 'Alice', 'Ng', 'alice@example.com', 'CFO')`,
		`INSERT INTO targets VALUES (2, 'Bob', 'Lee', 'bob@example.com', 'IT')`,
		`INSERT INTO "groups" VALUES (1, 1, 'Execs', '2021-01-01 10:00:00+00:00')`,
		`INSERT INTO group_targets VALUES (1, 1), (1, 2)`,
		`INSERT INTO templates VALUES (1, 1, 'Invoice', 'Pay {{.FirstName}}', 'text body', '<a href="{{.URL}}">pay</a>', '2021-01-01 10:00:00+00:00')`,
		`INSERT INTO attachments VALUES (1, 1, '` + attachment + `', 'text/plain', 'invoice.txt')`,
		`INSERT INTO pages VALUES (1, 1, 'Login', '<form></form>', 1, 0, 'https://example.com/thanks', '2021-01-01 10:00:00+00:00')`,
		`INSERT INTO smtp VALUES (1, 1, 'smtp', 'Relay', 'smtp.example.com:587', 'relay-user', 's3cret', '"IT" <it@example.com>', 0, '2021-01-01 10:00:00+00:00')`,
		`INSERT INTO headers VALUES (1, 'X-Custom', 'value', 1)`,
		`INSERT INTO campaigns VALUES (1, 1, 'Q4 2021', '2021-01-01 10:00:00+00:00', '2021-01-02 10:00:00+00:00', '2021-01-01 10:05:00+00:00', NULL, 1, 1, 1, 'Completed', 'https://phish.example.com')`,
		`INSERT INTO campaign_groups VALUES (1, 1)`,
		`INSERT INTO results VALUES (1, 1, 1, 'aaaaaaaa', 'alice@example.com', 'Alice', 'Ng', 'CFO', 'Submitted Data', '203.0.113.9', 0, '2021-01-01 10:06:00+00:00', '2021-01-01 10:20:00+00:00')`,
		`INSERT INTO results VALUES (2, 1, 1, 'bbbbbbbb', 'bob@example.com', 'Bob', 'Lee', 'IT', 'Email Sent', '203.0.113.9', 0, '2021-01-01 10:06:00+00:00', '2021-01-01 10:06:00+00:00')`,
		`INSERT INTO events VALUES (1, 1, 'alice@example.com', '2021-01-01 10:06:00+00:00', 'Email Sent', NULL)`,
		`INSERT INTO events VALUES (2, 1, 'alice@example.com', '2021-01-01 10:07:00+00:00', 'Email Opened', NULL)`,
		`INSERT INTO events VALUES (3, 1, 'alice@example.com', '2021-01-01 10:09:00+00:00', 'Clicked Link', NULL)`,
		`INSERT INTO events VALUES (4, 1, 'alice@example.com', '2021-01-01 10:10:00+00:00', 'Submitted Data', NULL)`,
		`INSERT INTO events VALUES (5, 1, 'alice@example.com', '2021-01-01 10:11:00+00:00', 'Email Reported', NULL)`,
		`INSERT INTO events VALUES (6, 1, 'bob@example.com', '2021-01-01 10:06:00+00:00', 'Email Sent', NULL)`,
		`INSERT INTO webhooks VALUES (1, 'SIEM', 'https://siem.example.com/hook', 'sig', 1)`,
	}
	for _, q := range stmts {
		if _, err := conn.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestImportGophishDatabase(t *testing.T) {
	dir := t.TempDir()
	buildLegacy(t, filepath.Join(dir, "gophish.db"))

	conn, err := db.Open(filepath.Join(dir, "trawlarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	box, _ := secret.New(make([]byte, 32))
	st := store.New(conn, box)

	rep, err := Import(context.Background(), Options{
		Source: filepath.Join(dir, "gophish.db"),
		Dest:   st,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Users != 2 || rep.Groups != 1 || rep.Targets != 2 || rep.Templates != 1 ||
		rep.Attachments != 1 || rep.Pages != 1 || rep.Profiles != 1 || rep.Campaigns != 1 ||
		rep.Results != 2 || rep.Events != 6 || rep.Webhooks != 1 {
		t.Fatalf("unexpected report: %+v", rep)
	}

	users, err := st.UsersByOrg(1)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{}
	for _, u := range users {
		roles[u.Username] = u.Role
	}
	if roles["admin"] != store.RoleAdmin || roles["analyst"] != store.RoleOperator {
		t.Fatalf("roles not mapped: %+v", roles)
	}

	groups, err := st.Groups(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Targets) != 2 {
		t.Fatalf("group import failed: %+v", groups)
	}

	templates, _ := st.Templates(1)
	if len(templates) != 1 || templates[0].Subject != "Pay {{.FirstName}}" {
		t.Fatalf("template import failed: %+v", templates)
	}
	if len(templates[0].Attachments) != 1 {
		t.Fatalf("attachment link lost: %+v", templates[0].Attachments)
	}

	profiles, _ := st.Profiles(1)
	if len(profiles) != 1 {
		t.Fatal("profile not imported")
	}
	if profiles[0].Host != "smtp.example.com" || profiles[0].Port != 587 {
		t.Fatalf("host:port not split: %+v", profiles[0])
	}
	if profiles[0].Password != "s3cret" {
		t.Fatalf("password not readable: %q", profiles[0].Password)
	}
	if len(profiles[0].Headers) != 1 || profiles[0].Headers[0].Key != "X-Custom" {
		t.Fatalf("headers not imported: %+v", profiles[0].Headers)
	}

	campaigns, _ := st.Campaigns(1, 10)
	if len(campaigns) != 1 {
		t.Fatal("campaign not imported")
	}
	c := campaigns[0]
	if c.Status != store.StatusCompleted {
		t.Fatalf("completed campaign should stay completed, got %q", c.Status)
	}
	if len(c.GroupIDs) != 1 {
		t.Fatalf("campaign audience lost: %+v", c.GroupIDs)
	}

	full, err := st.Campaign(1, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if full.Stats.Total != 2 || full.Stats.Opened != 1 || full.Stats.Submitted != 1 {
		t.Fatalf("stats not derived from events: %+v", full.Stats)
	}
	if len(full.Timeline) != 6 {
		t.Fatalf("timeline not imported: %d", len(full.Timeline))
	}

	hooks, _ := st.Webhooks(1)
	if len(hooks) != 1 || !hooks[0].Enabled {
		t.Fatalf("webhook import failed: %+v", hooks)
	}
}

func TestImportRefusesNonEmptyDestination(t *testing.T) {
	dir := t.TempDir()
	buildLegacy(t, filepath.Join(dir, "gophish.db"))
	conn, err := db.Open(filepath.Join(dir, "trawlarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	box, _ := secret.New(make([]byte, 32))
	st := store.New(conn, box)
	if _, err := st.Seed("admin", "hash"); err != nil {
		t.Fatal(err)
	}
	_, err = Import(context.Background(), Options{Source: filepath.Join(dir, "gophish.db"), Dest: st})
	if err == nil {
		t.Fatal("expected refusal without -force")
	}
}
