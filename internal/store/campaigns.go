package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Campaign statuses.
const (
	StatusDraft     = "draft"
	StatusScheduled = "scheduled"
	StatusQueued    = "queued"
	StatusSending   = "sending"
	StatusCompleted = "completed"
	StatusStopped   = "stopped"
	StatusError     = "error"
	// StatusUncertain marks a delivery where the SMTP session broke after the
	// message was handed over: it may or may not have arrived.
	StatusUncertain = "uncertain"
)

// Event types. These names are also the webhook event names.
const (
	EvCampaignStarted   = "campaign.started"
	EvCampaignCompleted = "campaign.completed"
	EvCampaignStopped   = "campaign.stopped"
	EvEmailSent         = "email.sent"
	EvEmailFailed       = "email.failed"
	EvEmailOpened       = "email.opened"
	EvLinkClicked       = "link.clicked"
	EvDataSubmitted     = "data.submitted"
	EvEmailReported     = "email.reported"
	EvTargetOptedOut    = "target.opted_out"
	EvSessionCaptured   = "session.captured"
)

// AllEvents lists every webhook event name.
var AllEvents = []string{
	EvCampaignStarted, EvCampaignCompleted, EvCampaignStopped,
	EvEmailSent, EvEmailFailed, EvEmailOpened, EvLinkClicked,
	EvDataSubmitted, EvEmailReported, EvTargetOptedOut, EvSessionCaptured,
}

// Campaign couples a template, landing page, sending profile and audience.
type Campaign struct {
	ID         int64   `json:"id"`
	OrgID      int64   `json:"org_id"`
	Name       string  `json:"name"`
	TemplateID int64   `json:"template_id"`
	PageID     int64   `json:"page_id"`
	ProfileID  int64   `json:"profile_id"`
	GroupIDs   []int64 `json:"group_ids"`
	URL        string  `json:"url"`
	// ProxyURL switches the campaign to relay mode: the landing page is fetched
	// live from this origin and session cookies are captured.
	ProxyURL       string `json:"proxy_url"`
	CaptureCookies bool   `json:"capture_cookies"`
	Status         string `json:"status"`
	LaunchAt       Time   `json:"launch_at"`
	SendBy         Time   `json:"send_by"`
	SendRate       int    `json:"send_rate"`
	Jitter         int    `json:"jitter_seconds"`
	ResendFailed   bool   `json:"resend_failed"`
	CreatedBy      int64  `json:"created_by"`
	CreatedAt      Time   `json:"created_at"`
	UpdatedAt      Time   `json:"updated_at"`
	StartedAt      Time   `json:"started_at"`
	CompletedAt    Time   `json:"completed_at"`

	Stats    Stats            `json:"stats"`
	Targets  []CampaignTarget `json:"results,omitempty"`
	Timeline []Event          `json:"timeline,omitempty"`
}

// Stats is the per-campaign funnel.
type Stats struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	Sent      int `json:"sent"`
	Opened    int `json:"opened"`
	Clicked   int `json:"clicked"`
	Submitted int `json:"submitted"`
	Reported  int `json:"reported"`
	Failed    int `json:"failed"`
	OptedOut  int `json:"opted_out"`
	Uncertain int `json:"uncertain"`
}

// CampaignTarget is one recipient's state inside a campaign.
type CampaignTarget struct {
	ID          int64             `json:"id"`
	CampaignID  int64             `json:"campaign_id"`
	RID         string            `json:"rid"`
	Email       string            `json:"email"`
	FirstName   string            `json:"first_name"`
	LastName    string            `json:"last_name"`
	Position    string            `json:"position"`
	Custom      map[string]string `json:"custom"`
	Status      string            `json:"status"`
	Error       string            `json:"error"`
	Attempts    int               `json:"attempts"`
	SentAt      Time              `json:"sent_at"`
	OpenedAt    Time              `json:"opened_at"`
	ClickedAt   Time              `json:"clicked_at"`
	SubmittedAt Time              `json:"submitted_at"`
	ReportedAt  Time              `json:"reported_at"`
	Fields      []string          `json:"fields,omitempty"`
	Data        Vector            `json:"data,omitempty"`
	IP          string            `json:"ip"`
	UserAgent   string            `json:"user_agent"`
	HasSession  bool              `json:"has_session"`
}

// Event is one timeline entry.
type Event struct {
	ID         int64  `json:"id"`
	CampaignID int64  `json:"campaign_id"`
	Email      string `json:"email"`
	Type       string `json:"type"`
	Message    string `json:"message"`
	IP         string `json:"ip"`
	UserAgent  string `json:"user_agent"`
	At         Time   `json:"at"`
}

const campaignCols = `id, org_id, name, template_id, page_id, profile_id, group_ids, url, proxy_url,
	capture_cookies, status, launch_at, send_by, send_rate, jitter_seconds, resend_failed, created_by,
	created_at, updated_at, started_at, completed_at`

func scanCampaign(row interface{ Scan(...any) error }) (*Campaign, error) {
	var c struct {
		Campaign
		groupIDs string
	}
	var launchAt, sendBy, createdAt, updatedAt, startedAt, completedAt int64
	err := row.Scan(&c.ID, &c.OrgID, &c.Name, &c.TemplateID, &c.PageID, &c.ProfileID, &c.groupIDs, &c.URL,
		&c.ProxyURL, &c.CaptureCookies, &c.Status, &launchAt, &sendBy, &c.SendRate, &c.Jitter, &c.ResendFailed, &c.CreatedBy,
		&createdAt, &updatedAt, &startedAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.GroupIDs = parseInts(c.groupIDs)
	c.LaunchAt = unixTime(launchAt)
	c.SendBy = unixTime(sendBy)
	c.CreatedAt = unixTime(createdAt)
	c.UpdatedAt = unixTime(updatedAt)
	c.StartedAt = unixTime(startedAt)
	c.CompletedAt = unixTime(completedAt)
	return &c.Campaign, nil
}

// Campaigns lists campaigns with stats, newest first.
func (s *Store) Campaigns(orgID int64, limit int) ([]*Campaign, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT `+campaignCols+` FROM campaigns WHERE org_id = ? ORDER BY id DESC LIMIT ?`, orgID, limit)
	if err != nil {
		return nil, err
	}
	out := []*Campaign{}
	ids := []int64{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
		ids = append(ids, c.ID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	stats, err := s.statsFor(ids...)
	if err != nil {
		return nil, err
	}
	for _, c := range out {
		c.Stats = stats[c.ID]
	}
	return out, nil
}

func (s *Store) statsFor(ids ...int64) (map[int64]Stats, error) {
	out := map[int64]Stats{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	// #nosec G202 -- only "?" placeholders are concatenated; ids are bound below.
	rows, err := s.db.Query(`SELECT campaign_id,
			COUNT(*),
			SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END),
			SUM(CASE WHEN sent_at > 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN opened_at > 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN clicked_at > 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN submitted_at > 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN reported_at > 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END),
			SUM(CASE WHEN status = 'opted_out' THEN 1 ELSE 0 END),
			SUM(CASE WHEN status = 'uncertain' THEN 1 ELSE 0 END)
		FROM campaign_targets WHERE campaign_id IN (`+placeholders+`) GROUP BY campaign_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var st Stats
		var pending, sent, opened, clicked, submitted, reported, failed, optedOut, uncertain sql.NullInt64
		if err := rows.Scan(&id, &st.Total, &pending, &sent, &opened, &clicked, &submitted, &reported, &failed, &optedOut, &uncertain); err != nil {
			return nil, err
		}
		st.Pending, st.Sent, st.Opened, st.Clicked = int(nullInt(pending)), int(nullInt(sent)), int(nullInt(opened)), int(nullInt(clicked))
		st.Submitted, st.Reported, st.Failed, st.OptedOut = int(nullInt(submitted)), int(nullInt(reported)), int(nullInt(failed)), int(nullInt(optedOut))
		st.Uncertain = int(nullInt(uncertain))
		out[id] = st
	}
	return out, rows.Err()
}

// Campaign fetches a campaign with stats, results and timeline.
func (s *Store) Campaign(orgID, id int64) (*Campaign, error) {
	c, err := scanCampaign(s.db.QueryRow(`SELECT `+campaignCols+` FROM campaigns WHERE id = ? AND org_id = ?`, id, orgID))
	if err != nil {
		return nil, err
	}
	stats, err := s.statsFor(id)
	if err != nil {
		return nil, err
	}
	c.Stats = stats[id]
	c.Targets, err = s.CampaignTargets(id)
	if err != nil {
		return nil, err
	}
	// Never surface captured passwords unless the landing page opted in.
	if page, err := s.Page(orgID, c.PageID); err == nil && !page.CapturePasswords {
		for i := range c.Targets {
			for _, k := range []string{"password", "passwd", "pwd", "pass"} {
				delete(c.Targets[i].Data, k)
			}
		}
	}
	for i := range c.Targets {
		c.Targets[i].HasSession = s.HasProxySession(c.Targets[i].RID)
	}
	c.Timeline, err = s.Events(id, 500)
	return c, err
}

// CampaignTargets lists recipient rows for a campaign.
func (s *Store) CampaignTargets(campaignID int64) ([]CampaignTarget, error) {
	rows, err := s.db.Query(`SELECT id, campaign_id, rid, email, first_name, last_name, position, custom, status, error,
		attempts, sent_at, opened_at, clicked_at, submitted_at, reported_at, submit_data, capture_fields, ip, user_agent
		FROM campaign_targets WHERE campaign_id = ? ORDER BY email`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CampaignTarget{}
	for rows.Next() {
		var t CampaignTarget
		var custom, data, fields string
		var sentAt, openedAt, clickedAt, submittedAt, reportedAt int64
		if err := rows.Scan(&t.ID, &t.CampaignID, &t.RID, &t.Email, &t.FirstName, &t.LastName, &t.Position, &custom,
			&t.Status, &t.Error, &t.Attempts, &sentAt, &openedAt, &clickedAt, &submittedAt, &reportedAt,
			&data, &fields, &t.IP, &t.UserAgent); err != nil {
			return nil, err
		}
		t.Custom = jsonMap(custom)
		t.Fields = parseStrings(fields)
		t.SentAt = unixTime(sentAt)
		t.OpenedAt = unixTime(openedAt)
		t.ClickedAt = unixTime(clickedAt)
		t.SubmittedAt = unixTime(submittedAt)
		t.ReportedAt = unixTime(reportedAt)
		if data != "" {
			if plain, err := s.box.Open(data); err == nil && len(plain) > 0 {
				var v Vector
				if json.Unmarshal(plain, &v) == nil {
					t.Data = v
				}
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveCampaign creates or updates a campaign definition.
func (s *Store) SaveCampaign(c *Campaign, id int64) error {
	if id == 0 {
		res, err := s.db.Exec(`INSERT INTO campaigns (org_id, name, template_id, page_id, profile_id, group_ids, url,
			proxy_url, capture_cookies, status, launch_at, send_by, send_rate, jitter_seconds, resend_failed,
			created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.OrgID, c.Name, c.TemplateID, c.PageID, c.ProfileID, jsonInts(c.GroupIDs), c.URL, c.ProxyURL,
			c.CaptureCookies, c.Status,
			c.LaunchAt.Time().Unix(), c.SendBy.Time().Unix(), c.SendRate, c.Jitter, c.ResendFailed, c.CreatedBy, now(), now())
		if err != nil {
			return err
		}
		c.ID, _ = res.LastInsertId()
		return nil
	}
	res, err := s.db.Exec(`UPDATE campaigns SET name = ?, template_id = ?, page_id = ?, profile_id = ?, group_ids = ?,
		url = ?, proxy_url = ?, capture_cookies = ?, status = ?, launch_at = ?, send_by = ?, send_rate = ?,
		jitter_seconds = ?, resend_failed = ?, updated_at = ? WHERE id = ? AND org_id = ?`,
		c.Name, c.TemplateID, c.PageID, c.ProfileID, jsonInts(c.GroupIDs), c.URL, c.ProxyURL, c.CaptureCookies, c.Status,
		c.LaunchAt.Time().Unix(), c.SendBy.Time().Unix(), c.SendRate, c.Jitter, c.ResendFailed, now(), id, c.OrgID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	c.ID = id
	return nil
}

// CampaignStatus reads just the status, used by the runner to notice a stop
// issued by another process (the MCP server, for example).
func (s *Store) CampaignStatus(id int64) (string, error) {
	var status string
	err := s.db.QueryRow(`SELECT status FROM campaigns WHERE id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return status, err
}

// SetCampaignStatus updates the lifecycle state.
func (s *Store) SetCampaignStatus(id int64, status string) error {
	now := now()
	q := `UPDATE campaigns SET status = ?, updated_at = ?`
	switch status {
	case StatusSending:
		q += `, started_at = CASE WHEN started_at = 0 THEN ? ELSE started_at END`
		_, err := s.db.Exec(q+` WHERE id = ?`, status, now, now, id)
		return err
	case StatusCompleted, StatusStopped, StatusError:
		q += `, completed_at = ?`
		_, err := s.db.Exec(q+` WHERE id = ?`, status, now, now, id)
		return err
	}
	_, err := s.db.Exec(q+` WHERE id = ?`, status, now, id)
	return err
}

// DeleteCampaign removes a campaign and its results.
func (s *Store) DeleteCampaign(orgID, id int64) error {
	return s.deleteScoped("campaigns", orgID, id)
}

// Events lists a campaign timeline, newest first.
func (s *Store) Events(campaignID int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT e.id, e.campaign_id, COALESCE(t.email, ''), e.type, e.message, e.ip, e.user_agent, e.at
		FROM events e LEFT JOIN campaign_targets t ON t.rid = e.rid
		WHERE e.campaign_id = ? ORDER BY e.id DESC LIMIT ?`, campaignID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at int64
		if err := rows.Scan(&e.ID, &e.CampaignID, &e.Email, &e.Type, &e.Message, &e.IP, &e.UserAgent, &at); err != nil {
			return nil, err
		}
		e.At = unixTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}
