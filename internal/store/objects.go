package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// ---------------------------------------------------------------- recipients

// Target is one recipient inside a group.
type Target struct {
	ID        int64             `json:"id"`
	GroupID   int64             `json:"group_id"`
	Email     string            `json:"email"`
	FirstName string            `json:"first_name"`
	LastName  string            `json:"last_name"`
	Position  string            `json:"position"`
	Custom    map[string]string `json:"custom"`
}

// Group is a reusable list of recipients.
type Group struct {
	ID        int64    `json:"id"`
	OrgID     int64    `json:"org_id"`
	Name      string   `json:"name"`
	Targets   []Target `json:"targets"`
	CreatedAt Time     `json:"created_at"`
	UpdatedAt Time     `json:"updated_at"`
}

func jsonMap(v string) map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal([]byte(v), &m)
	return m
}

func (s *Store) loadTargets(groupIDs ...int64) (map[int64][]Target, error) {
	out := map[int64][]Target{}
	for _, gid := range groupIDs {
		rows, err := s.db.Query(`SELECT id, group_id, email, first_name, last_name, position, custom
			FROM targets WHERE group_id = ? ORDER BY id`, gid)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var t Target
			var custom string
			if err := rows.Scan(&t.ID, &t.GroupID, &t.Email, &t.FirstName, &t.LastName, &t.Position, &custom); err != nil {
				rows.Close()
				return nil, err
			}
			t.Custom = jsonMap(custom)
			out[gid] = append(out[gid], t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Groups lists recipient groups with their targets.
func (s *Store) Groups(orgID int64) ([]*Group, error) {
	rows, err := s.db.Query(`SELECT id, org_id, name, created_at, updated_at FROM target_groups
		WHERE org_id = ? ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	var out []*Group
	ids := []int64{}
	for rows.Next() {
		var g Group
		var created, updated int64
		if err := rows.Scan(&g.ID, &g.OrgID, &g.Name, &created, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		g.CreatedAt = unixTime(created)
		g.UpdatedAt = unixTime(updated)
		out = append(out, &g)
		ids = append(ids, g.ID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	targets, err := s.loadTargets(ids...)
	if err != nil {
		return nil, err
	}
	for _, g := range out {
		g.Targets = targets[g.ID]
		if g.Targets == nil {
			g.Targets = []Target{}
		}
	}
	return out, nil
}

// Group fetches one group with its targets.
func (s *Store) Group(orgID, id int64) (*Group, error) {
	var g Group
	var created, updated int64
	err := s.db.QueryRow(`SELECT id, org_id, name, created_at, updated_at FROM target_groups
		WHERE id = ? AND org_id = ?`, id, orgID).Scan(&g.ID, &g.OrgID, &g.Name, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	g.CreatedAt = unixTime(created)
	g.UpdatedAt = unixTime(updated)
	targets, err := s.loadTargets(id)
	if err != nil {
		return nil, err
	}
	g.Targets = targets[id]
	if g.Targets == nil {
		g.Targets = []Target{}
	}
	return &g, nil
}

// SaveGroup creates or replaces a group and its target list in one transaction.
func (s *Store) SaveGroup(g *Group, id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if id == 0 {
		res, err := tx.Exec(`INSERT INTO target_groups (org_id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			g.OrgID, g.Name, now(), now())
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
	} else {
		res, err := tx.Exec(`UPDATE target_groups SET name = ?, updated_at = ? WHERE id = ? AND org_id = ?`,
			g.Name, now(), id, g.OrgID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(`DELETE FROM targets WHERE group_id = ?`, id); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, t := range g.Targets {
		email := t.Email
		if email == "" || seen[email] {
			continue
		}
		seen[email] = true
		if _, err := tx.Exec(`INSERT INTO targets (group_id, email, first_name, last_name, position, custom)
			VALUES (?, ?, ?, ?, ?, ?)`, id, email, t.FirstName, t.LastName, t.Position, MustJSON(orEmptyMap(t.Custom))); err != nil {
			return err
		}
	}
	g.ID = id
	return tx.Commit()
}

func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// DeleteGroup removes a group and its targets.
func (s *Store) DeleteGroup(orgID, id int64) error {
	return s.deleteScoped("target_groups", orgID, id)
}

// ---------------------------------------------------------------- templates

// Attachment is an uploadable file attached to templates.
type Attachment struct {
	ID          int64  `json:"id"`
	OrgID       int64  `json:"org_id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	CreatedAt   Time   `json:"created_at"`
}

// Template is an email body with personalisation placeholders.
type Template struct {
	ID             int64        `json:"id"`
	OrgID          int64        `json:"org_id"`
	Name           string       `json:"name"`
	Subject        string       `json:"subject"`
	TextBody       string       `json:"text_body"`
	HTMLBody       string       `json:"html_body"`
	Attachments    []int64      `json:"attachments"`
	AttachmentInfo []Attachment `json:"attachment_info,omitempty"`
	CreatedAt      Time         `json:"created_at"`
	UpdatedAt      Time         `json:"updated_at"`
}

// Templates lists email templates.
func (s *Store) Templates(orgID int64) ([]*Template, error) {
	rows, err := s.db.Query(`SELECT id, org_id, name, subject, text_body, html_body, attachments, created_at, updated_at
		FROM templates WHERE org_id = ? ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Template{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanTemplate(row interface{ Scan(...any) error }) (*Template, error) {
	var t Template
	var attachments string
	var created, updated int64
	err := row.Scan(&t.ID, &t.OrgID, &t.Name, &t.Subject, &t.TextBody, &t.HTMLBody, &attachments, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Attachments = parseInts(attachments)
	t.CreatedAt = unixTime(created)
	t.UpdatedAt = unixTime(updated)
	return &t, nil
}

// Template fetches one template.
func (s *Store) Template(orgID, id int64) (*Template, error) {
	t, err := scanTemplate(s.db.QueryRow(`SELECT id, org_id, name, subject, text_body, html_body, attachments, created_at, updated_at
		FROM templates WHERE id = ? AND org_id = ?`, id, orgID))
	if err != nil {
		return nil, err
	}
	if len(t.Attachments) > 0 {
		all, err := s.Attachments(orgID)
		if err != nil {
			return nil, err
		}
		byID := map[int64]Attachment{}
		for _, a := range all {
			byID[a.ID] = a
		}
		for _, aid := range t.Attachments {
			if a, ok := byID[aid]; ok {
				t.AttachmentInfo = append(t.AttachmentInfo, a)
			}
		}
	}
	return t, nil
}

// SaveTemplate creates or updates a template.
func (s *Store) SaveTemplate(t *Template, id int64) error {
	if id == 0 {
		res, err := s.db.Exec(`INSERT INTO templates (org_id, name, subject, text_body, html_body, attachments, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, t.OrgID, t.Name, t.Subject, t.TextBody, t.HTMLBody, jsonInts(t.Attachments), now(), now())
		if err != nil {
			return err
		}
		t.ID, _ = res.LastInsertId()
		return nil
	}
	res, err := s.db.Exec(`UPDATE templates SET name = ?, subject = ?, text_body = ?, html_body = ?, attachments = ?, updated_at = ?
		WHERE id = ? AND org_id = ?`, t.Name, t.Subject, t.TextBody, t.HTMLBody, jsonInts(t.Attachments), now(), id, t.OrgID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	t.ID = id
	return nil
}

// DeleteTemplate removes a template.
func (s *Store) DeleteTemplate(orgID, id int64) error {
	return s.deleteScoped("templates", orgID, id)
}

// -------------------------------------------------------------- attachments

// Attachments lists attachment metadata (never the bytes).
func (s *Store) Attachments(orgID int64) ([]Attachment, error) {
	rows, err := s.db.Query(`SELECT id, org_id, name, content_type, size, created_at FROM attachments
		WHERE org_id = ? ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		var a Attachment
		var created int64
		if err := rows.Scan(&a.ID, &a.OrgID, &a.Name, &a.ContentType, &a.Size, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = unixTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateAttachment stores a file.
func (s *Store) CreateAttachment(orgID int64, name, contentType string, content []byte) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO attachments (org_id, name, content_type, content, size, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, orgID, name, contentType, content, len(content), now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AttachmentContent loads bytes for a stored attachment.
func (s *Store) AttachmentContent(orgID, id int64) (string, string, []byte, error) {
	var name, ctype string
	var content []byte
	err := s.db.QueryRow(`SELECT name, content_type, content FROM attachments WHERE id = ? AND org_id = ?`,
		id, orgID).Scan(&name, &ctype, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil, ErrNotFound
	}
	return name, ctype, content, err
}

// DeleteAttachment removes a file.
func (s *Store) DeleteAttachment(orgID, id int64) error {
	return s.deleteScoped("attachments", orgID, id)
}

// -------------------------------------------------------------------- pages

// Page is a landing page served to recipients who click a link.
type Page struct {
	ID               int64  `json:"id"`
	OrgID            int64  `json:"org_id"`
	Name             string `json:"name"`
	HTML             string `json:"html"`
	Capture          bool   `json:"capture"`
	CapturePasswords bool   `json:"capture_passwords"`
	// CaptureValues stores what was typed. Off by default: field names alone
	// are enough for training metrics and keep real secrets out of the database.
	CaptureValues bool   `json:"capture_values"`
	RedirectURL   string `json:"redirect_url"`
	CreatedAt     Time   `json:"created_at"`
	UpdatedAt     Time   `json:"updated_at"`
}

// Pages lists landing pages.
func (s *Store) Pages(orgID int64) ([]*Page, error) {
	rows, err := s.db.Query(`SELECT id, org_id, name, html, capture, capture_passwords, capture_values, redirect_url, created_at, updated_at
		FROM pages WHERE org_id = ? ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Page{}
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanPage(row interface{ Scan(...any) error }) (*Page, error) {
	var p Page
	var created, updated int64
	err := row.Scan(&p.ID, &p.OrgID, &p.Name, &p.HTML, &p.Capture, &p.CapturePasswords, &p.CaptureValues, &p.RedirectURL, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt = unixTime(created)
	p.UpdatedAt = unixTime(updated)
	return &p, nil
}

// Page fetches one landing page.
func (s *Store) Page(orgID, id int64) (*Page, error) {
	return scanPage(s.db.QueryRow(`SELECT id, org_id, name, html, capture, capture_passwords, capture_values, redirect_url, created_at, updated_at
		FROM pages WHERE id = ? AND org_id = ?`, id, orgID))
}

// SavePage creates or updates a landing page.
func (s *Store) SavePage(p *Page, id int64) error {
	if id == 0 {
		res, err := s.db.Exec(`INSERT INTO pages (org_id, name, html, capture, capture_passwords, capture_values, redirect_url, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.OrgID, p.Name, p.HTML, p.Capture, p.CapturePasswords, p.CaptureValues, p.RedirectURL, now(), now())
		if err != nil {
			return err
		}
		p.ID, _ = res.LastInsertId()
		return nil
	}
	res, err := s.db.Exec(`UPDATE pages SET name = ?, html = ?, capture = ?, capture_passwords = ?, capture_values = ?,
		redirect_url = ?, updated_at = ? WHERE id = ? AND org_id = ?`,
		p.Name, p.HTML, p.Capture, p.CapturePasswords, p.CaptureValues, p.RedirectURL, now(), id, p.OrgID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	p.ID = id
	return nil
}

// DeletePage removes a landing page.
func (s *Store) DeletePage(orgID, id int64) error {
	return s.deleteScoped("pages", orgID, id)
}

// --------------------------------------------------------- sending profiles

// Header is a custom SMTP header.
type Header struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Profile holds SMTP settings. Password is write-only via the API.
type Profile struct {
	ID             int64    `json:"id"`
	OrgID          int64    `json:"org_id"`
	Name           string   `json:"name"`
	Host           string   `json:"host"`
	Port           int      `json:"port"`
	Username       string   `json:"username"`
	Password       string   `json:"password,omitempty"`
	HasPassword    bool     `json:"has_password"`
	FromAddress    string   `json:"from_address"`
	EnvelopeSender string   `json:"envelope_sender"`
	IgnoreCert     bool     `json:"ignore_cert"`
	Headers        []Header `json:"headers"`
	LastTestAt     Time     `json:"last_test_at"`
	LastTestError  string   `json:"last_test_error"`
	CreatedAt      Time     `json:"created_at"`
	UpdatedAt      Time     `json:"updated_at"`
}

// Profiles lists sending profiles.
func (s *Store) Profiles(orgID int64) ([]*Profile, error) {
	rows, err := s.db.Query(`SELECT id, org_id, name, host, port, username, password, from_address, envelope_sender,
		ignore_cert, headers, last_test_at, last_test_error, created_at, updated_at FROM profiles WHERE org_id = ? ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Profile{}
	for rows.Next() {
		p, err := s.scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) scanProfile(row interface{ Scan(...any) error }) (*Profile, error) {
	var p Profile
	var encPassword, headers string
	var lastTest, created, updated int64
	err := row.Scan(&p.ID, &p.OrgID, &p.Name, &p.Host, &p.Port, &p.Username, &encPassword,
		&p.FromAddress, &p.EnvelopeSender, &p.IgnoreCert, &headers, &lastTest, &p.LastTestError, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if plain, err := s.box.Open(encPassword); err == nil {
		p.Password = string(plain)
	}
	p.HasPassword = encPassword != ""
	p.LastTestAt = unixTime(lastTest)
	_ = json.Unmarshal([]byte(headers), &p.Headers)
	p.CreatedAt = unixTime(created)
	p.UpdatedAt = unixTime(updated)
	return &p, nil
}

// Profile fetches one sending profile.
func (s *Store) Profile(orgID, id int64) (*Profile, error) {
	return s.scanProfile(s.db.QueryRow(`SELECT id, org_id, name, host, port, username, password, from_address,
		envelope_sender, ignore_cert, headers, last_test_at, last_test_error, created_at, updated_at
		FROM profiles WHERE id = ? AND org_id = ?`, id, orgID))
}

// SaveProfile creates or updates a sending profile. An empty password keeps
// the stored one, so the UI never has to round-trip secrets.
func (s *Store) SaveProfile(p *Profile, id int64) error {
	var enc string
	if p.Password != "" {
		var err error
		if enc, err = s.box.Seal([]byte(p.Password)); err != nil {
			return err
		}
	}
	headers := []byte("[]")
	if p.Headers != nil {
		headers, _ = json.Marshal(p.Headers)
	}
	if id == 0 {
		res, err := s.db.Exec(`INSERT INTO profiles (org_id, name, host, port, username, password, from_address,
			envelope_sender, ignore_cert, headers, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.OrgID, p.Name, p.Host, p.Port, p.Username, enc, p.FromAddress, p.EnvelopeSender, p.IgnoreCert, string(headers), now(), now())
		if err != nil {
			return err
		}
		p.ID, _ = res.LastInsertId()
		return nil
	}
	var encPassword any
	if enc != "" {
		encPassword = enc // empty keeps the stored password
	}
	res, err := s.db.Exec(`UPDATE profiles SET name = ?, host = ?, port = ?, username = ?,
		password = COALESCE(?, password), from_address = ?, envelope_sender = ?, ignore_cert = ?, headers = ?, updated_at = ?
		WHERE id = ? AND org_id = ?`, p.Name, p.Host, p.Port, p.Username, encPassword, p.FromAddress, p.EnvelopeSender,
		p.IgnoreCert, string(headers), now(), id, p.OrgID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	p.ID = id
	return nil
}

// SetProfileTest records the outcome of a test send.
func (s *Store) SetProfileTest(orgID, id int64, errText string) error {
	_, err := s.db.Exec(`UPDATE profiles SET last_test_at = ?, last_test_error = ? WHERE id = ? AND org_id = ?`,
		now(), truncate(errText, 500), id, orgID)
	return err
}

// DeleteProfile removes a sending profile.
func (s *Store) DeleteProfile(orgID, id int64) error {
	return s.deleteScoped("profiles", orgID, id)
}
