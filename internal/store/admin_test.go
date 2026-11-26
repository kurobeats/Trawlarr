package store_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kurobeats/trawlarr/internal/store"
)

func TestUserAdministration(t *testing.T) {
	s := newStore(t)
	admin, err := s.UserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(admin.PasswordHash) == 0 {
		t.Fatal("seeded user has no password hash")
	}

	u := &store.User{OrgID: 1, Username: "Analyst", Email: "a@example.com", Role: store.RoleViewer, PasswordHash: "hash"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	// Usernames are case-insensitive and unique.
	if _, err := s.UserByUsername("analyst"); err != nil {
		t.Fatalf("case-insensitive lookup failed: %v", err)
	}
	dup := &store.User{OrgID: 1, Username: "ANALYST", Role: store.RoleViewer, PasswordHash: "x"}
	if err := s.CreateUser(dup); err != store.ErrConflict {
		t.Fatalf("duplicate username error = %v, want ErrConflict", err)
	}
	// The store rejects a bad role before SQLite's CHECK constraint does.
	if err := s.CreateUser(&store.User{OrgID: 1, Username: "bad", Role: "root"}); err == nil {
		t.Fatal("invalid role accepted by the store")
	}

	u.Role = store.RoleOperator
	u.Email = "new@example.com"
	u.Disabled = true
	if err := s.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	got, _ := s.UserByID(u.ID)
	if got.Role != store.RoleOperator || got.Email != "new@example.com" || !got.Disabled {
		t.Fatalf("update not applied: %+v", got)
	}

	if err := s.SetPassword(u.ID, "new-hash"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.UserByID(u.ID)
	if got.PasswordHash != "new-hash" {
		t.Fatal("password not updated")
	}

	// A disabled account cannot authenticate with an API key either.
	if err := s.SetAPIKey(u.ID, store.HashToken("key")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserByAPIKeyHash(store.HashToken("key")); err == nil {
		t.Fatal("disabled user authenticated with an API key")
	}
	u.Disabled = false
	if err := s.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAPIKey(u.ID, store.HashToken("key2")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserByAPIKeyHash(store.HashToken("key2")); err != nil {
		t.Fatalf("API key lookup failed: %v", err)
	}
	if _, err := s.UserByAPIKeyHash(store.HashToken("wrong")); err == nil {
		t.Fatal("unknown API key resolved")
	}
	if err := s.SetAPIKey(u.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserByAPIKeyHash(store.HashToken("key2")); err == nil {
		t.Fatal("cleared API key still resolves")
	}

	users, err := s.UsersByOrg(1)
	if err != nil || len(users) != 2 {
		t.Fatalf("UsersByOrg = %d users, %v", len(users), err)
	}

	if n, err := s.CountAdmins(1); err != nil || n != 1 {
		t.Fatalf("CountAdmins = %d, %v", n, err)
	}
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserByID(u.ID); err != store.ErrNotFound {
		t.Fatalf("deleted user still resolves: %v", err)
	}
}

func TestSessionsAndExpiry(t *testing.T) {
	s := newStore(t)
	u, _ := s.UserByUsername("admin")
	hash := store.HashToken("live-token")
	if err := s.CreateSession(hash, u.ID, time.Hour, "127.0.0.1", "ua"); err != nil {
		t.Fatal(err)
	}

	expired := store.HashToken("expired-token")
	if err := s.CreateSession(expired, u.ID, -time.Minute, "127.0.0.1", "ua"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySession(expired); err == nil {
		t.Fatal("expired session accepted")
	}

	// Disabling a user invalidates their live sessions.
	if err := s.SetTOTP(u.ID, "", false); err != nil {
		t.Fatal(err)
	}
	u.Disabled = true
	if err := s.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySession(hash); err == nil {
		t.Fatal("session for a disabled user accepted")
	}

	if err := s.PurgeSessions(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n > 1 {
		t.Fatalf("expired sessions not purged: %d remain", n)
	}
}

func TestFirstAdmin(t *testing.T) {
	s := newStore(t)
	u, err := s.FirstAdmin()
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "admin" {
		t.Fatalf("FirstAdmin = %q", u.Username)
	}
	if _, err := s.DB().Exec(`UPDATE users SET disabled = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FirstAdmin(); err == nil {
		t.Fatal("disabled admin returned as the MCP actor")
	}
}

func TestAttachmentLifecycle(t *testing.T) {
	s := newStore(t)
	id, err := s.CreateAttachment(1, "invoice.pdf", "application/pdf", []byte("%PDF"))
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.Attachments(1)
	if err != nil || len(list) != 1 {
		t.Fatalf("Attachments = %d, %v", len(list), err)
	}
	if list[0].Size != 4 {
		t.Fatalf("size = %d", list[0].Size)
	}
	name, ctype, content, err := s.AttachmentContent(1, id)
	if err != nil || name != "invoice.pdf" || ctype != "application/pdf" || string(content) != "%PDF" {
		t.Fatalf("content = %q %q %q %v", name, ctype, content, err)
	}
	if _, _, _, err := s.AttachmentContent(1, 999); err != store.ErrNotFound {
		t.Fatalf("missing attachment error = %v", err)
	}
	if err := s.DeleteAttachment(1, id); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.AttachmentContent(1, id); err != store.ErrNotFound {
		t.Fatal("deleted attachment still readable")
	}
}

func TestWebhookEventFiltering(t *testing.T) {
	s := newStore(t)
	if err := s.SaveWebhook(&store.Webhook{OrgID: 1, Name: "all", URL: "https://a.example/h", Events: []string{"*"}, Enabled: true}, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWebhook(&store.Webhook{OrgID: 1, Name: "opens", URL: "https://b.example/h", Events: []string{"email.opened"}, Enabled: true}, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWebhook(&store.Webhook{OrgID: 1, Name: "off", URL: "https://c.example/h", Events: []string{"*"}, Enabled: false}, 0); err != nil {
		t.Fatal(err)
	}

	matched, err := s.WebhooksForEvent(1, "email.opened")
	if err != nil || len(matched) != 2 {
		t.Fatalf("email.opened hooks = %d, %v", len(matched), err)
	}
	matched, _ = s.WebhooksForEvent(1, "link.clicked")
	if len(matched) != 1 || matched[0].Name != "all" {
		t.Fatalf("link.clicked hooks = %+v", matched)
	}

	// Updating without a secret keeps the stored one.
	hook, _ := s.Webhook(1, 1)
	hook.Secret = "s3cret"
	if err := s.SaveWebhook(hook, hook.ID); err != nil {
		t.Fatal(err)
	}
	hook.Name = "renamed"
	if err := s.SaveWebhook(hook, hook.ID); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Webhook(1, 1)
	if again.Secret != "s3cret" {
		t.Fatalf("secret lost on update: %q", again.Secret)
	}
	if !again.HasSecret {
		t.Fatal("has_secret not derived")
	}
	if err := s.DeleteWebhook(1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Webhook(1, 1); err != store.ErrNotFound {
		t.Fatal("deleted webhook still readable")
	}
}

func TestAuditLog(t *testing.T) {
	s := newStore(t)
	s.Audit(1, 0, "first", "t", "d", "127.0.0.1")
	s.Audit(1, 0, "second", "t", "d", "127.0.0.1")
	entries, err := s.AuditLog(1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Action != "second" {
		t.Fatalf("unexpected audit order: %+v", entries)
	}
	if _, err := s.AuditLog(2, 10); err != nil {
		t.Fatal(err)
	}
}

func TestProfileTestHistory(t *testing.T) {
	s := newStore(t)
	p := &store.Profile{OrgID: 1, Name: "p", Host: "h", Port: 587, FromAddress: "a@example.com"}
	if err := s.SaveProfile(p, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProfileTest(1, p.ID, "connection refused"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Profile(1, p.ID)
	if got.LastTestAt.IsZero() || got.LastTestError == "" {
		t.Fatalf("failure not recorded: %+v", got)
	}
	if err := s.SetProfileTest(1, p.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Profile(1, p.ID)
	if got.LastTestError != "" {
		t.Fatalf("success did not clear the error: %q", got.LastTestError)
	}
	if err := s.DeleteProfile(1, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Profile(1, p.ID); err != store.ErrNotFound {
		t.Fatal("deleted profile still readable")
	}
}

func TestResetFailedAndUncertain(t *testing.T) {
	s := newStore(t)
	g := &store.Group{OrgID: 1, Name: "g", Targets: []store.Target{{Email: "a@example.com"}, {Email: "b@example.com"}}}
	if err := s.SaveGroup(g, 0); err != nil {
		t.Fatal(err)
	}
	c := &store.Campaign{OrgID: 1, Name: "c", GroupIDs: []int64{g.ID}}
	if err := s.SaveCampaign(c, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MaterializeTargets(c); err != nil {
		t.Fatal(err)
	}
	targets, _ := s.CampaignTargets(c.ID)
	if err := s.MarkFailed(targets[0].ID, "refused"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUncertain(targets[1].ID, "timeout"); err != nil {
		t.Fatal(err)
	}
	// Both a definite failure (still under the retry cap) and an uncertain send
	// are eligible for another attempt.
	pending, err := s.PendingTargets(c.ID)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	n, err := s.ResetFailed(c.ID)
	if err != nil || n != 2 {
		t.Fatalf("ResetFailed = %d, %v", n, err)
	}
	pending, _ = s.PendingTargets(c.ID)
	if len(pending) != 2 {
		t.Fatalf("pending after reset = %d", len(pending))
	}
}

func TestProxySessionStore(t *testing.T) {
	s := newStore(t)
	g := &store.Group{OrgID: 1, Name: "g", Targets: []store.Target{{Email: "victim@example.com"}}}
	if err := s.SaveGroup(g, 0); err != nil {
		t.Fatal(err)
	}
	c := &store.Campaign{OrgID: 1, Name: "relay", GroupIDs: []int64{g.ID}, ProxyURL: "https://login.example.com", CaptureCookies: true}
	if err := s.SaveCampaign(c, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MaterializeTargets(c); err != nil {
		t.Fatal(err)
	}
	targets, _ := s.CampaignTargets(c.ID)
	rid := targets[0].RID

	if s.HasProxySession(rid) {
		t.Fatal("session reported before anything was captured")
	}
	if err := s.SaveProxySession(c.ID, rid, "victim@example.com", "login.example.com", map[string]string{"AUTH": "token"}); err != nil {
		t.Fatal(err)
	}
	if !s.HasProxySession(rid) {
		t.Fatal("captured session not visible")
	}
	cookies, err := s.ProxySession(rid, "login.example.com")
	if err != nil || cookies["AUTH"] != "token" {
		t.Fatalf("cookies = %v, %v", cookies, err)
	}
	upstream, cookies, err := s.ProxySessionForTarget(c.ID, targets[0].ID)
	if err != nil || upstream != "login.example.com" || cookies["AUTH"] != "token" {
		t.Fatalf("admin lookup = %q %v %v", upstream, cookies, err)
	}
	if _, _, err := s.ProxySessionForTarget(c.ID, 9999); err != store.ErrNotFound {
		t.Fatal("unknown target should not resolve")
	}

	// The cookies are encrypted at rest.
	var raw string
	if err := s.DB().QueryRow(`SELECT cookies FROM proxy_sessions WHERE rid = ?`, rid).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "token") {
		t.Fatal("session cookies stored in clear text")
	}
}

func TestSettingsAndDecryption(t *testing.T) {
	s := newStore(t)
	if got, err := s.Setting("missing"); err != nil || got != "" {
		t.Fatalf("missing setting = %q, %v", got, err)
	}
	if err := s.SetSetting("k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("k", "v2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Setting("k"); got != "v2" {
		t.Fatalf("setting = %q", got)
	}

	enc, err := s.Seal("mail-password")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.DecryptSetting(enc)
	if err != nil || plain != "mail-password" {
		t.Fatalf("DecryptSetting = %q, %v", plain, err)
	}
	if plain, err := s.DecryptSetting("legacy-plaintext"); err != nil || plain != "legacy-plaintext" {
		t.Fatalf("legacy passthrough = %q, %v", plain, err)
	}
}

func TestTimeJSONRoundTrip(t *testing.T) {
	var zero store.Time
	if b, err := json.Marshal(zero); err != nil || string(b) != "null" {
		t.Fatalf("zero time marshals as %s (%v)", b, err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	tv := store.TimeOf(now)
	b, err := json.Marshal(tv)
	if err != nil {
		t.Fatal(err)
	}
	var back store.Time
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Time().Equal(now) {
		t.Fatalf("round trip = %s, want %s", back.Time(), now)
	}
	// Stored form is unix seconds, zero-safe.
	v, err := tv.Value()
	if err != nil {
		t.Fatal(err)
	}
	if v.(int64) != now.Unix() {
		t.Fatalf("stored value = %v", v)
	}
	var scanned store.Time
	if err := scanned.Scan(v); err != nil {
		t.Fatal(err)
	}
	if !scanned.Time().Equal(now) {
		t.Fatalf("scan = %s", scanned.Time())
	}
	if err := scanned.Scan(nil); err != nil || !scanned.IsZero() {
		t.Fatalf("nil scan = %v %v", scanned, err)
	}
	if err := scanned.Scan("2021-01-02 03:04:05+00:00"); err != nil || scanned.IsZero() {
		t.Fatalf("string scan = %v %v", scanned, err)
	}
	if err := scanned.Scan(3.14); err == nil {
		t.Fatal("unexpected type accepted")
	}
	var bad store.Time
	if err := json.Unmarshal([]byte(`"not-a-time"`), &bad); err == nil {
		t.Fatal("invalid time accepted")
	}
}

func TestGroupValidationHelpers(t *testing.T) {
	s := newStore(t)
	g := &store.Group{OrgID: 1, Name: "g", Targets: []store.Target{{Email: "a@example.com"}}}
	if err := s.SaveGroup(g, 0); err != nil {
		t.Fatal(err)
	}
	// Updating a group in another org is a not-found, not a silent write.
	if err := s.SaveGroup(&store.Group{OrgID: 2, Name: "hijack"}, g.ID); err != store.ErrNotFound {
		t.Fatalf("cross-org update = %v", err)
	}
	// A delete scoped to another org reports a miss and leaves the group alone.
	if err := s.DeleteGroup(2, g.ID); err != store.ErrNotFound {
		t.Fatalf("cross-org delete = %v, want ErrNotFound", err)
	}
	if _, err := s.Group(1, g.ID); err != nil {
		t.Fatalf("cross-org delete removed the group: %v", err)
	}
	if g, err := s.Group(1, 999); err != store.ErrNotFound || g != nil {
		t.Fatalf("missing group = %v, %v", g, err)
	}
}
