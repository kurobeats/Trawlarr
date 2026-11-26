package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// SaveProxySession stores the relayed upstream cookies for one recipient.
func (s *Store) SaveProxySession(campaignID int64, rid, email, upstream string, cookies map[string]string) error {
	raw, err := json.Marshal(cookies)
	if err != nil {
		return err
	}
	enc, err := s.box.Seal(raw)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO proxy_sessions (campaign_id, rid, email, upstream, cookies, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(rid, upstream) DO UPDATE SET cookies = excluded.cookies, updated_at = excluded.updated_at`,
		campaignID, rid, email, upstream, enc, now(), now())
	return err
}

// ProxySession loads the stored cookies for a recipient.
func (s *Store) ProxySession(rid, upstream string) (map[string]string, error) {
	var enc string
	err := s.db.QueryRow(`SELECT cookies FROM proxy_sessions WHERE rid = ? AND upstream = ?`, rid, upstream).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	plain, err := s.box.Open(enc)
	if err != nil {
		return nil, err
	}
	cookies := map[string]string{}
	if err := json.Unmarshal(plain, &cookies); err != nil {
		return nil, err
	}
	return cookies, nil
}

// HasProxySession reports whether anything was captured for a recipient.
func (s *Store) HasProxySession(rid string) bool {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM proxy_sessions WHERE rid = ?`, rid).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// ProxySessionForTarget is the admin-facing lookup, scoped to a campaign.
func (s *Store) ProxySessionForTarget(campaignID, targetID int64) (string, map[string]string, error) {
	var rid string
	err := s.db.QueryRow(`SELECT rid FROM campaign_targets WHERE id = ? AND campaign_id = ?`,
		targetID, campaignID).Scan(&rid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	var upstream string
	err = s.db.QueryRow(`SELECT upstream FROM proxy_sessions WHERE rid = ? ORDER BY updated_at DESC LIMIT 1`, rid).Scan(&upstream)
	if errors.Is(err, sql.ErrNoRows) {
		return rid, map[string]string{}, nil
	}
	if err != nil {
		return rid, nil, err
	}
	cookies, err := s.ProxySession(rid, upstream)
	if err != nil {
		return rid, nil, err
	}
	return upstream, cookies, nil
}
