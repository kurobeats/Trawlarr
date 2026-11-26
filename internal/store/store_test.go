package store_test

import (
	"testing"

	"github.com/kurobeats/trawlarr/internal/db"
	"github.com/kurobeats/trawlarr/internal/secret"
	"github.com/kurobeats/trawlarr/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	conn, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s := store.New(conn, box)
	if _, err := s.Seed("admin", "hash"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCampaignLifecycle(t *testing.T) {
	s := newStore(t)
	orgID := int64(1)

	g := &store.Group{OrgID: orgID, Name: "Execs", Targets: []store.Target{
		{Email: "alice@example.com", FirstName: "Alice", LastName: "Ng"},
		{Email: "bob@example.com", FirstName: "Bob"},
		{Email: "ALICE@example.com", FirstName: "Duplicate"}, // deduped
	}}
	if err := s.SaveGroup(g, 0); err != nil {
		t.Fatal(err)
	}
	tmpl := &store.Template{OrgID: orgID, Name: "T", Subject: "Hi {{.FirstName}}", HTMLBody: "<p>{{.Tracker}}</p>"}
	if err := s.SaveTemplate(tmpl, 0); err != nil {
		t.Fatal(err)
	}
	page := &store.Page{OrgID: orgID, Name: "P", HTML: "<form></form>", Capture: true, CapturePasswords: false}
	if err := s.SavePage(page, 0); err != nil {
		t.Fatal(err)
	}
	prof := &store.Profile{OrgID: orgID, Name: "S", Host: "smtp.example.com", Port: 587,
		FromAddress: "IT <it@example.com>", Password: "relay-secret"}
	if err := s.SaveProfile(prof, 0); err != nil {
		t.Fatal(err)
	}

	// The stored SMTP password is encrypted, and reading it back works.
	stored, err := s.Profile(orgID, prof.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Password != "relay-secret" {
		t.Fatalf("password round-trip failed: %q", stored.Password)
	}

	c := &store.Campaign{OrgID: orgID, Name: "Q4", TemplateID: tmpl.ID, PageID: page.ID, ProfileID: prof.ID, GroupIDs: []int64{g.ID}}
	if err := s.SaveCampaign(c, 0); err != nil {
		t.Fatal(err)
	}
	n, err := s.MaterializeTargets(c)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 recipients, got %d", n)
	}

	targets, err := s.CampaignTargets(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := targets[0]
	if len(first.RID) < 10 {
		t.Fatalf("recipient id too short to be unguessable: %q", first.RID)
	}
	if err := s.MarkSent(first.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Track(&first, store.EvEmailOpened, "Email opened", "203.0.113.5", "test", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Track(&first, store.EvLinkClicked, "Clicked", "203.0.113.5", "test", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Track(&first, store.EvDataSubmitted, "Submitted", "203.0.113.5", "test",
		&store.Capture{Fields: []string{"password", "username"}, Values: map[string]string{"username": "alice", "password": "hunter2"}},
		false); err != nil {
		t.Fatal(err)
	}

	got, err := s.Campaign(orgID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.Sent != 1 || got.Stats.Opened != 1 || got.Stats.Clicked != 1 || got.Stats.Submitted != 1 {
		t.Fatalf("unexpected stats: %+v", got.Stats)
	}
	if got.Stats.Total != 2 {
		t.Fatalf("total recipients: %+v", got.Stats)
	}
	for _, r := range got.Targets {
		if r.RID != first.RID {
			continue
		}
		if _, leaked := r.Data["password"]; leaked {
			t.Fatal("password was returned although the page does not capture passwords")
		}
		if r.Data["username"] != "alice" {
			t.Fatalf("captured data missing: %+v", r.Data)
		}
		if len(r.Fields) != 2 {
			t.Fatalf("field names should be recorded even without values: %+v", r.Fields)
		}
		if r.OpenedAt.IsZero() || r.ClickedAt.IsZero() || r.SubmittedAt.IsZero() {
			t.Fatalf("milestones not recorded: %+v", r)
		}
	}
}

func TestPasswordCapturedWhenEnabled(t *testing.T) {
	s := newStore(t)
	orgID := int64(1)
	g := &store.Group{OrgID: orgID, Name: "G", Targets: []store.Target{{Email: "a@example.com"}}}
	if err := s.SaveGroup(g, 0); err != nil {
		t.Fatal(err)
	}
	page := &store.Page{OrgID: orgID, Name: "P", Capture: true, CapturePasswords: true}
	if err := s.SavePage(page, 0); err != nil {
		t.Fatal(err)
	}
	c := &store.Campaign{OrgID: orgID, Name: "C", PageID: page.ID, GroupIDs: []int64{g.ID}}
	if err := s.SaveCampaign(c, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MaterializeTargets(c); err != nil {
		t.Fatal(err)
	}
	targets, _ := s.CampaignTargets(c.ID)
	if err := s.Track(&targets[0], store.EvDataSubmitted, "Submitted", "127.0.0.1", "ua",
		&store.Capture{Fields: []string{"password"}, Values: map[string]string{"password": "hunter2"}}, true); err != nil {
		t.Fatal(err)
	}
	got, err := s.Campaign(orgID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Targets[0].Data["password"] != "hunter2" {
		t.Fatalf("password should be captured, got %+v", got.Targets[0].Data)
	}
}

func TestOptOutExcludesRecipients(t *testing.T) {
	s := newStore(t)
	orgID := int64(1)
	if err := s.OptOut("bob@example.com"); err != nil {
		t.Fatal(err)
	}
	g := &store.Group{OrgID: orgID, Name: "G", Targets: []store.Target{
		{Email: "alice@example.com"},
		{Email: "Bob@Example.com"},
	}}
	if err := s.SaveGroup(g, 0); err != nil {
		t.Fatal(err)
	}
	c := &store.Campaign{OrgID: orgID, Name: "C", GroupIDs: []int64{g.ID}}
	if err := s.SaveCampaign(c, 0); err != nil {
		t.Fatal(err)
	}
	n, err := s.MaterializeTargets(c)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("opted-out address should be skipped, got %d recipients", n)
	}
}

func TestTimeZeroRoundTrip(t *testing.T) {
	s := newStore(t)
	c := &store.Campaign{OrgID: 1, Name: "C", Status: store.StatusDraft}
	if err := s.SaveCampaign(c, 0); err != nil {
		t.Fatal(err)
	}
	got, err := s.Campaign(1, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LaunchAt.IsZero() || !got.CompletedAt.IsZero() {
		t.Fatalf("unset timestamps should stay zero: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("creation timestamp missing")
	}
}

func TestSessionRoundTrip(t *testing.T) {
	s := newStore(t)
	u, err := s.UserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	hash := store.HashToken("token-value")
	if err := s.CreateSession(hash, u.ID, 60e9, "127.0.0.1", "test"); err != nil {
		t.Fatal(err)
	}
	got, err := s.UserBySession(hash)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != u.ID {
		t.Fatalf("wrong user for session: %d", got.ID)
	}
	if err := s.DeleteSession(hash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySession(hash); err == nil {
		t.Fatal("deleted session still resolves")
	}
}

func TestGeoAnnotationOnFirstInteraction(t *testing.T) {
	s := newStore(t)
	orgID := int64(1)
	calls := 0
	s.SetLocator(func(ip string) (float64, float64, string, string, bool) {
		calls++
		if ip == "203.0.113.5" {
			return 51.5, -0.12, "London", "United Kingdom", true
		}
		return 0, 0, "", "", false
	})

	g := &store.Group{OrgID: orgID, Name: "G", Targets: []store.Target{{Email: "alice@example.com"}}}
	if err := s.SaveGroup(g, 0); err != nil {
		t.Fatal(err)
	}
	c := &store.Campaign{OrgID: orgID, Name: "C", GroupIDs: []int64{g.ID}}
	if err := s.SaveCampaign(c, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MaterializeTargets(c); err != nil {
		t.Fatal(err)
	}
	targets, _ := s.CampaignTargets(c.ID)
	target := targets[0]

	if err := s.Track(&target, store.EvEmailOpened, "open", "203.0.113.5", "ua", nil, false); err != nil {
		t.Fatal(err)
	}
	got, err := s.Campaign(orgID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	row := got.Targets[0]
	if row.Latitude == 0 || row.Longitude == 0 || row.City != "London" || row.Country != "United Kingdom" {
		t.Fatalf("location not stored: %+v", row)
	}

	// A second interaction must not move the pin.
	if err := s.Track(&target, store.EvLinkClicked, "click", "198.51.100.2", "ua", nil, false); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Campaign(orgID, c.ID)
	if got.Targets[0].City != "London" {
		t.Fatalf("first location overwritten: %+v", got.Targets[0])
	}
	if calls < 2 {
		t.Fatalf("locator calls = %d", calls)
	}
}
