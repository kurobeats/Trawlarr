package store

import (
	"database/sql"
	"errors"
)

// Webhook receives signed JSON when a campaign event fires.
type Webhook struct {
	ID        int64    `json:"id"`
	OrgID     int64    `json:"org_id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Secret    string   `json:"secret,omitempty"`
	HasSecret bool     `json:"has_secret"`
	Events    []string `json:"events"`
	Enabled   bool     `json:"enabled"`
	CreatedAt Time     `json:"created_at"`
	UpdatedAt Time     `json:"updated_at"`
}

// Webhooks lists an org's webhooks.
func (s *Store) Webhooks(orgID int64) ([]*Webhook, error) {
	rows, err := s.db.Query(`SELECT id, org_id, name, url, secret, events, enabled, created_at, updated_at
		FROM webhooks WHERE org_id = ? ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Webhook{}
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func scanWebhook(row interface{ Scan(...any) error }) (*Webhook, error) {
	var w Webhook
	var events string
	var created, updated int64
	err := row.Scan(&w.ID, &w.OrgID, &w.Name, &w.URL, &w.Secret, &events, &w.Enabled, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	w.Events = parseStrings(events)
	w.HasSecret = w.Secret != ""
	w.CreatedAt = unixTime(created)
	w.UpdatedAt = unixTime(updated)
	return &w, nil
}

// Webhook fetches one webhook.
func (s *Store) Webhook(orgID, id int64) (*Webhook, error) {
	return scanWebhook(s.db.QueryRow(`SELECT id, org_id, name, url, secret, events, enabled, created_at, updated_at
		FROM webhooks WHERE id = ? AND org_id = ?`, id, orgID))
}

// SaveWebhook creates or updates a webhook. An empty secret keeps the old one.
func (s *Store) SaveWebhook(w *Webhook, id int64) error {
	if id == 0 {
		res, err := s.db.Exec(`INSERT INTO webhooks (org_id, name, url, secret, events, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, w.OrgID, w.Name, w.URL, w.Secret, jsonStrings(w.Events), w.Enabled, now(), now())
		if err != nil {
			return err
		}
		w.ID, _ = res.LastInsertId()
		return nil
	}
	var secret any = w.Secret
	if w.Secret == "" {
		secret = nil
	}
	res, err := s.db.Exec(`UPDATE webhooks SET name = ?, url = ?, secret = COALESCE(?, secret), events = ?, enabled = ?, updated_at = ?
		WHERE id = ? AND org_id = ?`, w.Name, w.URL, secret, jsonStrings(w.Events), w.Enabled, now(), id, w.OrgID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	w.ID = id
	return nil
}

// DeleteWebhook removes a webhook.
func (s *Store) DeleteWebhook(orgID, id int64) error {
	_, err := s.db.Exec(`DELETE FROM webhooks WHERE id = ? AND org_id = ?`, id, orgID)
	return err
}

// WebhooksForEvent returns enabled hooks subscribed to an event type (or "*").
func (s *Store) WebhooksForEvent(orgID int64, event string) ([]*Webhook, error) {
	all, err := s.Webhooks(orgID)
	if err != nil {
		return nil, err
	}
	out := []*Webhook{}
	for _, w := range all {
		if !w.Enabled {
			continue
		}
		for _, e := range w.Events {
			if e == event || e == "*" {
				out = append(out, w)
				break
			}
		}
	}
	return out, nil
}
