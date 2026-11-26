package store

import (
	"database/sql"
	"errors"
)

// Certificate is a profile for one Let's Encrypt certificate: the domains it
// covers, who to contact, and where the issued material is cached. Campaigns
// reference the profile that covers their tracking hostname; the TLS listener
// picks the certificate by SNI.
type Certificate struct {
	ID        int64    `json:"id"`
	OrgID     int64    `json:"org_id"`
	Name      string   `json:"name"`
	Domains   []string `json:"domains"`
	Email     string   `json:"email"`
	Staging   bool     `json:"staging"`
	AcceptTOS bool     `json:"accept_tos"`
	CacheDir  string   `json:"cache_dir"`
	HTTPAddr  string   `json:"http_addr"`
	CreatedAt Time     `json:"created_at"`
	UpdatedAt Time     `json:"updated_at"`
}

const certificateCols = `id, org_id, name, domains, email, staging, accept_tos, cache_dir, http_addr,
	created_at, updated_at`

func scanCertificate(row interface{ Scan(...any) error }) (*Certificate, error) {
	var c Certificate
	var domains string
	var created, updated int64
	err := row.Scan(&c.ID, &c.OrgID, &c.Name, &domains, &c.Email, &c.Staging, &c.AcceptTOS,
		&c.CacheDir, &c.HTTPAddr, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Domains = parseStrings(domains)
	c.CreatedAt = unixTime(created)
	c.UpdatedAt = unixTime(updated)
	return &c, nil
}

// Certificates lists an org's certificate profiles.
func (s *Store) Certificates(orgID int64) ([]*Certificate, error) {
	rows, err := s.db.Query(`SELECT `+certificateCols+` FROM certificates WHERE org_id = ? ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Certificate{}
	for rows.Next() {
		c, err := scanCertificate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AllCertificates lists every profile, used by the TLS listener for SNI lookup.
func (s *Store) AllCertificates() ([]*Certificate, error) {
	rows, err := s.db.Query(`SELECT ` + certificateCols + ` FROM certificates`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Certificate{}
	for rows.Next() {
		c, err := scanCertificate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Certificate fetches one profile.
func (s *Store) Certificate(orgID, id int64) (*Certificate, error) {
	return scanCertificate(s.db.QueryRow(`SELECT `+certificateCols+` FROM certificates WHERE id = ? AND org_id = ?`, id, orgID))
}

// SaveCertificate creates or updates a profile.
func (s *Store) SaveCertificate(c *Certificate, id int64) error {
	domains := jsonStrings(c.Domains)
	if id == 0 {
		res, err := s.db.Exec(`INSERT INTO certificates (org_id, name, domains, email, staging, accept_tos,
			cache_dir, http_addr, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.OrgID, c.Name, domains, c.Email, c.Staging, c.AcceptTOS, c.CacheDir, c.HTTPAddr, now(), now())
		if err != nil {
			return err
		}
		c.ID, _ = res.LastInsertId()
		return nil
	}
	res, err := s.db.Exec(`UPDATE certificates SET name = ?, domains = ?, email = ?, staging = ?, accept_tos = ?,
		cache_dir = ?, http_addr = ?, updated_at = ? WHERE id = ? AND org_id = ?`,
		c.Name, domains, c.Email, c.Staging, c.AcceptTOS, c.CacheDir, c.HTTPAddr, now(), id, c.OrgID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	c.ID = id
	return nil
}

// DeleteCertificate removes a profile that no campaign references.
func (s *Store) DeleteCertificate(orgID, id int64) error {
	var used int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM campaigns WHERE certificate_id = ? AND org_id = ?`, id, orgID).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return ErrConflict
	}
	return s.deleteScoped("certificates", orgID, id)
}

// CertificateForHost returns the profile covering a hostname, used to validate
// a campaign before launch.
func (s *Store) CertificateForHost(orgID int64, host string) (*Certificate, error) {
	all, err := s.Certificates(orgID)
	if err != nil {
		return nil, err
	}
	host = lower(host)
	for _, c := range all {
		for _, d := range c.Domains {
			if lower(d) == host {
				return c, nil
			}
		}
	}
	return nil, ErrNotFound
}

// CertificateUsage counts campaigns pointing at a profile.
func (s *Store) CertificateUsage(orgID, id int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM campaigns WHERE certificate_id = ? AND org_id = ?`, id, orgID).Scan(&n)
	return n, err
}
