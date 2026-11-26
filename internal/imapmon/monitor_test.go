package imapmon

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/emersion/go-imap"

	"github.com/kurobeats/trawlarr/internal/config"
	"github.com/kurobeats/trawlarr/internal/db"
	"github.com/kurobeats/trawlarr/internal/secret"
	"github.com/kurobeats/trawlarr/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	conn, err := db.Open(t.TempDir() + "/imap.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	box, _ := secret.New(make([]byte, 32))
	st := store.New(conn, box)
	if _, err := st.Seed("admin", "hash"); err != nil {
		t.Fatal(err)
	}
	return st
}

// targetRID creates a campaign recipient and returns its id.
func targetRID(t *testing.T, st *store.Store) string {
	t.Helper()
	g := &store.Group{OrgID: 1, Name: "G", Targets: []store.Target{{Email: "victim@example.com"}}}
	if err := st.SaveGroup(g, 0); err != nil {
		t.Fatal(err)
	}
	c := &store.Campaign{OrgID: 1, Name: "C", GroupIDs: []int64{g.ID}}
	if err := st.SaveCampaign(c, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MaterializeTargets(c); err != nil {
		t.Fatal(err)
	}
	targets, err := st.CampaignTargets(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return targets[0].RID
}

func TestSettingsDefaults(t *testing.T) {
	st := newStore(t)
	s := Load(st, config.Default())
	if s.Enabled() {
		t.Fatal("monitor enabled without configuration")
	}
	if s.Port != 993 || s.Folder != "INBOX" || s.Interval != 5*time.Minute {
		t.Fatalf("unexpected defaults: %+v", s)
	}
}

func TestSettingsFromConfig(t *testing.T) {
	st := newStore(t)
	cfg := config.Default()
	cfg.IMAP = config.IMAP{
		Host: "imap.example.com", Port: 143, Username: "reports", Password: "pw",
		Folder: "Phish", UseTLS: true, DeleteReported: true, IntervalMinutes: 2,
	}
	s := Load(st, cfg)
	if !s.Enabled() || s.Host != "imap.example.com" || s.Port != 143 || s.Folder != "Phish" {
		t.Fatalf("config values not used: %+v", s)
	}
	if s.Interval != 2*time.Minute || !s.Delete {
		t.Fatalf("interval/delete not applied: %+v", s)
	}
}

func TestSettingsFromImportedDatabase(t *testing.T) {
	st := newStore(t)
	// The database importer writes these keys.
	enc, err := st.Seal("imported-password")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"imap_host": "mail.internal", "imap_port": "1993", "imap_username": "soc",
		"imap_password": enc, "imap_folder": "Reports",
	} {
		if err := st.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	s := Load(st, config.Default())
	if s.Host != "mail.internal" || s.Port != 1993 || s.Username != "soc" || s.Folder != "Reports" {
		t.Fatalf("imported settings not applied: %+v", s)
	}
	if s.Password != "imported-password" {
		t.Fatalf("imported password not decrypted: %q", s.Password)
	}
}

func TestDisabledSettingWins(t *testing.T) {
	st := newStore(t)
	cfg := config.Default()
	cfg.IMAP.Host = "imap.example.com"
	cfg.IMAP.Username = "reports"
	_ = st.SetSetting("imap_enabled", "false")
	if s := Load(st, cfg); s.Enabled() {
		t.Fatal("imap_enabled=false did not disable the monitor")
	}
}

func TestRIDMatching(t *testing.T) {
	st := newStore(t)
	rid := targetRID(t, st)
	m := &Monitor{}

	cases := []struct {
		name string
		env  *imap.Envelope
		want string
	}{
		{"message id", &imap.Envelope{MessageId: "<" + rid + ".deadbeef@example.com>"}, rid},
		{"in reply to", &imap.Envelope{InReplyTo: "<" + rid + ".cafe@example.com>"}, rid},
		{"subject fallback", &imap.Envelope{Subject: "Fwd: invoice " + rid}, rid},
		{"unknown rid", &imap.Envelope{MessageId: "<aaaaaaaaaaaa.dead@example.com>"}, ""},
		{"wrong length", &imap.Envelope{MessageId: "<" + rid[:6] + "@example.com>"}, ""},
		{"no headers", &imap.Envelope{}, ""},
	}
	for _, c := range cases {
		if got := m.ridFromEnvelope(c.env, st); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPollSkipsUnresolvableMailbox(t *testing.T) {
	st := newStore(t)
	m := &Monitor{Settings: Settings{Host: "127.0.0.1", Port: 1, Username: "x", Password: "y", Interval: time.Second}}
	// Port 1 is not an IMAP server; poll must return an error, not panic.
	if _, err := m.poll(st); err == nil {
		t.Fatal("expected a connection error")
	}
}

func TestRunExitsImmediatelyWhenDisabled(t *testing.T) {
	st := newStore(t)
	done := make(chan struct{})
	go func() {
		(&Monitor{Settings: Settings{}}).Run(nil, st)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run blocked with no mailbox configured")
	}
}

func TestRunPollsAndSurvivesErrors(t *testing.T) {
	st := newStore(t)
	m := &Monitor{
		Settings: Settings{Host: "127.0.0.1", Port: 1, Username: "x", Password: "y", Interval: 10 * time.Millisecond},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx, st)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}
