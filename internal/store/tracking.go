package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

// NewRID returns a short, unguessable recipient id for tracking URLs.
func NewRID() string {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// MaterializeTargets expands a campaign's groups into one row per recipient.
// Existing rows are kept so re-launching never doubles up or changes links.
func (s *Store) MaterializeTargets(c *Campaign) (int, error) {
	var existing int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM campaign_targets WHERE campaign_id = ?`, c.ID).Scan(&existing); err != nil {
		return 0, err
	}
	if existing > 0 {
		return existing, nil
	}
	// Load recipients before opening the write transaction: SQLite allows a
	// single writer, so querying inside the tx would stall.
	var groups []*Group
	for _, gid := range c.GroupIDs {
		g, err := s.Group(c.OrgID, gid)
		if err != nil {
			return 0, err
		}
		groups = append(groups, g)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	optedOut := s.optedOutEmails()
	inserted := 0
	{
		for _, g := range groups {
		for _, t := range g.Targets {
			key := lower(t.Email)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			if _, opted := optedOut[key]; opted {
				continue
			}
			rid := uniqueRID(tx)
			if _, err := tx.Exec(`INSERT INTO campaign_targets
				(campaign_id, rid, email, first_name, last_name, position, custom, status, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
				c.ID, rid, t.Email, t.FirstName, t.LastName, t.Position, MustJSON(orEmptyMap(t.Custom)), now()); err != nil {
				return 0, err
			}
			inserted++
		}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func uniqueRID(tx *sql.Tx) string {
	for i := 0; i < 5; i++ {
		rid := NewRID()
		var n int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM campaign_targets WHERE rid = ?`, rid).Scan(&n)
		if n == 0 {
			return rid
		}
	}
	return NewRID()
}

func lower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// PendingTargets returns recipients still awaiting an email.
func (s *Store) PendingTargets(campaignID int64) ([]CampaignTarget, error) {
	all, err := s.CampaignTargets(campaignID)
	if err != nil {
		return nil, err
	}
	out := []CampaignTarget{}
	for _, t := range all {
		if t.Status == "pending" || (t.Status == "failed" && t.Attempts < 3) || t.Status == "uncertain" {
			out = append(out, t)
		}
	}
	return out, nil
}

// ResetFailed requeues failed recipients, used by "resend failed".
func (s *Store) ResetFailed(campaignID int64) (int64, error) {
	res, err := s.db.Exec(`UPDATE campaign_targets SET status = 'pending', attempts = 0, error = '', updated_at = ?
		WHERE campaign_id = ? AND status IN ('failed', 'uncertain')`, now(), campaignID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DueCampaigns returns campaigns whose schedule has come around.
func (s *Store) DueCampaigns(at time.Time) ([]*Campaign, error) {
	rows, err := s.db.Query(`SELECT `+campaignCols+` FROM campaigns
		WHERE status IN ('queued', 'scheduled') AND launch_at > 0 AND launch_at <= ?`, at.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SendingCampaigns lists campaigns that a restarted process should resume.
func (s *Store) SendingCampaigns() ([]*Campaign, error) {
	rows, err := s.db.Query(`SELECT ` + campaignCols + ` FROM campaigns WHERE status = 'sending'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TargetByRID resolves a tracking id to its recipient and campaign.
func (s *Store) TargetByRID(rid string) (*CampaignTarget, *Campaign, error) {
	if rid == "" || len(rid) > 64 {
		return nil, nil, ErrNotFound
	}
	var t CampaignTarget
	var custom string
	var sentAt, openedAt, clickedAt, submittedAt, reportedAt int64
	err := s.db.QueryRow(`SELECT id, campaign_id, rid, email, first_name, last_name, position, custom, status, error,
		attempts, sent_at, opened_at, clicked_at, submitted_at, reported_at, ip, user_agent
		FROM campaign_targets WHERE rid = ?`, rid).Scan(&t.ID, &t.CampaignID, &t.RID, &t.Email, &t.FirstName,
		&t.LastName, &t.Position, &custom, &t.Status, &t.Error, &t.Attempts, &sentAt, &openedAt, &clickedAt,
		&submittedAt, &reportedAt, &t.IP, &t.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	t.Custom = jsonMap(custom)
	t.SentAt = unixTime(sentAt)
	t.OpenedAt = unixTime(openedAt)
	t.ClickedAt = unixTime(clickedAt)
	t.SubmittedAt = unixTime(submittedAt)
	t.ReportedAt = unixTime(reportedAt)
	c, err := scanCampaign(s.db.QueryRow(`SELECT `+campaignCols+` FROM campaigns WHERE id = ?`, t.CampaignID))
	if err != nil {
		return nil, nil, err
	}
	return &t, c, nil
}

// Track records an engagement event and advances the recipient's milestones.
// It is the only write path used by the public phishing endpoints.
// Capture is what a landing page submission recorded. Fields always names the
// submitted inputs; Values is only populated when the page opts into storing
// the entered values.
type Capture struct {
	Fields []string          `json:"fields"`
	Values map[string]string `json:"values,omitempty"`
}

func (s *Store) Track(t *CampaignTarget, evType, message, ip, ua string, data *Capture, capturePasswords bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO events (campaign_id, rid, type, message, ip, user_agent, at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, t.CampaignID, t.RID, evType, message, ip, ua, now()); err != nil {
		return err
	}
	sets := []string{"updated_at = ?"}
	args := []any{now()}
	touch := func(col string) {
		sets = append(sets, col+` = CASE WHEN `+col+` = 0 THEN ? ELSE `+col+` END`)
		args = append(args, now())
	}
	next := t.Status
	advance := func(status string) {
		if targetRank(status) > targetRank(next) {
			next = status
		}
	}
	switch evType {
	case EvEmailSent:
		touch("sent_at")
		advance("sent")
	case EvEmailFailed:
		next = "failed"
	case EvEmailOpened:
		touch("opened_at")
		advance("opened")
	case EvLinkClicked:
		touch("clicked_at")
		advance("clicked")
	case EvDataSubmitted:
		touch("submitted_at")
		advance("submitted")
		if data != nil {
			if !capturePasswords {
				for _, k := range []string{"password", "passwd", "pwd", "pass"} {
					delete(data.Values, k)
				}
			}
			if data.Values != nil {
				enc, err := s.box.Seal([]byte(MustJSON(data.Values)))
				if err != nil {
					return err
				}
				sets = append(sets, "submit_data = ?")
				args = append(args, enc)
			}
			sets = append(sets, "capture_fields = ?")
			args = append(args, jsonStrings(data.Fields))
		}
	case EvEmailReported:
		touch("reported_at")
		advance("reported")
	case EvTargetOptedOut:
		next = "opted_out"
	}
	if next != t.Status {
		sets = append(sets, "status = ?")
		args = append(args, next)
	}
	if ip != "" {
		sets = append(sets, "ip = ?", "user_agent = ?")
		args = append(args, ip, ua)
	}
	args = append(args, t.RID)
	if _, err := tx.Exec(`UPDATE campaign_targets SET `+strings.Join(sets, ", ")+` WHERE rid = ?`, args...); err != nil {
		return err
	}
	return tx.Commit()
}

func targetRank(status string) int {
	switch status {
	case "pending":
		return 0
	case "sent", "failed", "uncertain", "opted_out":
		return 1
	case "opened":
		return 2
	case "clicked":
		return 3
	case "submitted":
		return 4
	case "reported":
		return 5
	}
	return 0
}

// MarkSent records a successful SMTP hand-off.
func (s *Store) MarkSent(targetID int64, ip string) error {
	ts := now()
	_, err := s.db.Exec(`UPDATE campaign_targets SET status = 'sent', sent_at = ?, attempts = attempts + 1,
		error = '', updated_at = ? WHERE id = ? AND status = 'pending'`, ts, ts, targetID)
	return err
}

// MarkFailed records a delivery error that definitely did not deliver.
func (s *Store) MarkFailed(targetID int64, reason string) error {
	return s.markDelivery(targetID, "failed", reason)
}

// MarkUncertain records an error that happened after the message may have been
// handed to the remote server. Treating these as failed would make operators
// resend to people who already received the email.
func (s *Store) MarkUncertain(targetID int64, reason string) error {
	return s.markDelivery(targetID, "uncertain", reason)
}

func (s *Store) markDelivery(targetID int64, status, reason string) error {
	ts := now()
	_, err := s.db.Exec(`UPDATE campaign_targets SET status = ?, attempts = attempts + 1, error = ?, updated_at = ?
		WHERE id = ?`, status, truncate(reason, 500), ts, targetID)
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// AddEvent appends a timeline entry. Pass an empty rid for campaign-level
// events such as "campaign started".
func (s *Store) AddEvent(campaignID int64, rid, evType, message string) error {
	_, err := s.db.Exec(`INSERT INTO events (campaign_id, rid, type, message, at) VALUES (?, ?, ?, ?, ?)`,
		campaignID, rid, evType, message, now())
	return err
}

// EventTargetByEmail finds a recipient row by campaign and email address,
// used by the IMAP reporter to mark the right person.
func (s *Store) EventTargetByEmail(campaignID int64, email string) (*CampaignTarget, error) {
	var rid string
	err := s.db.QueryRow(`SELECT rid FROM campaign_targets WHERE campaign_id = ? AND lower(email) = ?`,
		campaignID, lower(email)).Scan(&rid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t, _, err := s.TargetByRID(rid)
	return t, err
}

// OptOut records a global do-not-contact request.
func (s *Store) OptOut(email string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO NOTHING`, "optout:"+lower(email), nowString())
	return err
}

func nowString() string { return time.Unix(now(), 0).UTC().Format(time.RFC3339) }

func (s *Store) optedOutEmails() map[string]struct{} {
	out := map[string]struct{}{}
	rows, err := s.db.Query(`SELECT key FROM settings WHERE key LIKE 'optout:%'`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out[lower(k[7:])] = struct{}{}
		}
	}
	return out
}

// IsOptedOut reports whether an address asked not to be contacted.
func (s *Store) IsOptedOut(email string) bool {
	_, ok := s.optedOutEmails()[lower(email)]
	return ok
}
