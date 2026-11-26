package engine_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kurobeats/trawlarr/internal/db"
	"github.com/kurobeats/trawlarr/internal/engine"
	"github.com/kurobeats/trawlarr/internal/mail"
	"github.com/kurobeats/trawlarr/internal/secret"
	"github.com/kurobeats/trawlarr/internal/store"
)

type capture struct {
	mu   sync.Mutex
	sent []mail.Message
	fail error
}

func (c *capture) Send(_ context.Context, _ *store.Profile, m mail.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail != nil {
		return c.fail
	}
	c.sent = append(c.sent, m)
	return nil
}

func (c *capture) messages() []mail.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]mail.Message(nil), c.sent...)
}

func setup(t *testing.T, sender mail.Sender) (*engine.Engine, *store.Store, *store.Campaign) {
	t.Helper()
	conn, err := db.Open(t.TempDir() + "/engine.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(conn, box)
	if _, err := st.Seed("admin", "hash"); err != nil {
		t.Fatal(err)
	}

	g := &store.Group{OrgID: 1, Name: "G", Targets: []store.Target{
		{Email: "alice@example.com", FirstName: "Alice"},
		{Email: "bob@example.com", FirstName: "Bob"},
	}}
	if err := st.SaveGroup(g, 0); err != nil {
		t.Fatal(err)
	}
	tpl := &store.Template{OrgID: 1, Name: "T", Subject: "Hi {{.FirstName}}",
		HTMLBody: `<a href="{{.URL}}">x</a><img src="{{.TrackingURL}}"/>`}
	if err := st.SaveTemplate(tpl, 0); err != nil {
		t.Fatal(err)
	}
	page := &store.Page{OrgID: 1, Name: "P", HTML: "<form></form>"}
	if err := st.SavePage(page, 0); err != nil {
		t.Fatal(err)
	}
	prof := &store.Profile{OrgID: 1, Name: "S", Host: "localhost", Port: 2525, FromAddress: "it@example.com"}
	if err := st.SaveProfile(prof, 0); err != nil {
		t.Fatal(err)
	}
	c := &store.Campaign{OrgID: 1, Name: "C", TemplateID: tpl.ID, PageID: page.ID,
		ProfileID: prof.ID, GroupIDs: []int64{g.ID}}
	if err := st.SaveCampaign(c, 0); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, sender, nil, testLogger(), func(*store.Campaign) string {
		return "https://phish.example.com"
	})
	return eng, st, c
}

func TestCampaignSendsEveryRecipient(t *testing.T) {
	sender := &capture{}
	eng, st, c := setup(t, sender)
	if err := eng.Launch(c); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, st, c.ID, store.StatusCompleted)

	msgs := sender.messages()
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}
	for _, m := range msgs {
		if !strings.Contains(m.HTML, "https://phish.example.com/click?rid=") {
			t.Fatalf("tracked link missing: %s", m.HTML)
		}
		if !strings.Contains(m.Subject, "Hi ") {
			t.Fatalf("template not rendered: %q", m.Subject)
		}
	}
	got, err := st.Campaign(1, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.Sent != 2 || len(got.Timeline) < 3 {
		t.Fatalf("unexpected result: stats=%+v events=%d", got.Stats, len(got.Timeline))
	}
}

func TestDeliveryFailureIsRecorded(t *testing.T) {
	sender := &capture{fail: errors.New("dial tcp: connection refused")}
	eng, st, c := setup(t, sender)
	if err := eng.Launch(c); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, st, c.ID, store.StatusCompleted)
	got, err := st.Campaign(1, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.Failed != 2 {
		t.Fatalf("want 2 failures, got %+v", got.Stats)
	}
	if got.Targets[0].Error == "" {
		t.Fatal("failure reason not stored")
	}
}

func TestUncertainDeliveryIsNotMarkedFailed(t *testing.T) {
	sender := &capture{fail: context.DeadlineExceeded}
	eng, st, c := setup(t, sender)
	if err := eng.Launch(c); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, st, c.ID, store.StatusCompleted)
	got, err := st.Campaign(1, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.Failed != 0 || got.Stats.Uncertain != 2 {
		t.Fatalf("timeouts should be uncertain, not failed: %+v", got.Stats)
	}
}

func TestStopCancelsCampaign(t *testing.T) {
	sender := &capture{}
	eng, st, c := setup(t, sender)
	c.SendRate = 3600 // one per second, so there is time to stop
	if err := st.SaveCampaign(c, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := eng.Launch(c); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	eng.Stop(c.ID)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := st.Campaign(1, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == store.StatusStopped {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("campaign did not stop")
}

func TestLaunchValidation(t *testing.T) {
	eng, _, c := setup(t, &capture{})
	c.GroupIDs = nil
	if err := eng.Launch(c); err == nil {
		t.Fatal("campaign without groups should not launch")
	}
	c.GroupIDs = []int64{1}
	c.TemplateID = 999
	if err := eng.Launch(c); err == nil {
		t.Fatal("campaign with a missing template should not launch")
	}
}

func waitForStatus(t *testing.T, st *store.Store, id int64, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		c, err := st.Campaign(1, id)
		if err != nil {
			t.Fatal(err)
		}
		if c.Status == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	got, _ := st.Campaign(1, id)
	t.Fatalf("campaign did not reach %q (status %q)", want, got.Status)
}

func TestRunningReportsActiveCampaign(t *testing.T) {
	sender := &capture{}
	eng, st, c := setup(t, sender)
	c.SendRate = 3600
	if err := st.SaveCampaign(c, c.ID); err != nil {
		t.Fatal(err)
	}
	if eng.Running(c.ID) {
		t.Fatal("campaign reported as running before launch")
	}
	if err := eng.Launch(c); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !eng.Running(c.ID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !eng.Running(c.ID) {
		t.Fatal("running campaign not reported")
	}
	eng.Stop(c.ID)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !eng.Running(c.ID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("campaign stayed registered after stop")
}

func TestSchedulerPicksUpDueCampaign(t *testing.T) {
	sender := &capture{}
	eng, st, c := setup(t, sender)
	c.LaunchAt = store.TimeOf(time.Now().Add(-time.Minute))
	if err := st.SaveCampaign(c, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCampaignStatus(c.ID, store.StatusScheduled); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Start(ctx)
	waitForStatus(t, st, c.ID, store.StatusCompleted)
	if len(sender.messages()) != 2 {
		t.Fatalf("scheduled campaign sent %d messages", len(sender.messages()))
	}
}

func TestResumeAfterRestart(t *testing.T) {
	sender := &capture{}
	_, st, c := setup(t, sender)
	if err := st.SaveCampaign(c, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MaterializeTargets(c); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCampaignStatus(c.ID, store.StatusSending); err != nil {
		t.Fatal(err)
	}

	// A fresh engine (as after a restart) resumes what was in flight.
	eng := engine.New(st, sender, nil, testLogger(), func(*store.Campaign) string {
		return "https://phish.example.com"
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Start(ctx)
	waitForStatus(t, st, c.ID, store.StatusCompleted)
	if len(sender.messages()) != 2 {
		t.Fatalf("resumed campaign sent %d messages", len(sender.messages()))
	}
}

func TestLaunchRejectsCompletedCampaign(t *testing.T) {
	eng, st, c := setup(t, &capture{})
	if err := st.SaveCampaign(c, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCampaignStatus(c.ID, store.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	c.Status = store.StatusCompleted
	if err := eng.Launch(c); err == nil {
		t.Fatal("completed campaign relaunched")
	}
}

func TestUncertainClassification(t *testing.T) {
	cases := map[string]bool{
		"i/o timeout":                             true,
		"read tcp: connection reset by peer":      true,
		"unexpected EOF":                          true,
		"dial tcp 1.2.3.4:25: connection refused": false,
		"smtp: 535 authentication failed":         false,
		"no such host":                            false,
	}
	for msg, want := range cases {
		if got := engine.IsUncertain(errors.New(msg)); got != want {
			t.Errorf("isUncertainPhase(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestPaceInterval(t *testing.T) {
	base := &store.Campaign{}
	if got := engine.PaceInterval(base, 10); got != 0 {
		t.Fatalf("unpaced campaign = %s", got)
	}
	rated := &store.Campaign{SendRate: 3600}
	if got := engine.PaceInterval(rated, 100); got != time.Second {
		t.Fatalf("rate pacing = %s, want 1s", got)
	}
	window := &store.Campaign{SendBy: store.TimeOf(time.Now().Add(10 * time.Minute))}
	got := engine.PaceInterval(window, 10)
	if got < 50*time.Second || got > 70*time.Second {
		t.Fatalf("send-by pacing = %s, want about a minute", got)
	}
	past := &store.Campaign{SendBy: store.TimeOf(time.Now().Add(-time.Hour))}
	if engine.PaceInterval(past, 5) != 0 {
		t.Fatal("expired window should not add delay")
	}
}
