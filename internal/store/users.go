package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Roles, ordered from most to least privileged.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// Rank returns a permission weight for a role.
func Rank(role string) int {
	switch role {
	case RoleAdmin:
		return 3
	case RoleOperator:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// User is an operator account.
type User struct {
	ID           int64  `json:"id"`
	OrgID        int64  `json:"org_id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	PasswordHash string `json:"-"`
	TOTPSecret   string `json:"-"`
	TOTPEnabled  bool   `json:"totp_enabled"`
	APIKeyHash   string `json:"-"`
	HasAPIKey    bool   `json:"has_api_key"`
	Disabled     bool   `json:"disabled"`
	CreatedAt    Time   `json:"created_at"`
	UpdatedAt    Time   `json:"updated_at"`
}

const userCols = `id, org_id, username, email, role, password_hash, totp_secret, totp_enabled, api_key_hash, disabled, created_at, updated_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created, updated int64
	err := row.Scan(&u.ID, &u.OrgID, &u.Username, &u.Email, &u.Role, &u.PasswordHash,
		&u.TOTPSecret, &u.TOTPEnabled, &u.APIKeyHash, &u.Disabled, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.HasAPIKey = u.APIKeyHash != ""
	u.CreatedAt = unixTime(created)
	u.UpdatedAt = unixTime(updated)
	return &u, nil
}

// UserByUsername looks a user up case-insensitively.
func (s *Store) UserByUsername(username string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?`, username))
}

// UserByID looks a user up by primary key.
func (s *Store) UserByID(id int64) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// UserByAPIKeyHash finds the owner of an API key.
func (s *Store) UserByAPIKeyHash(hash string) (*User, error) {
	if hash == "" {
		return nil, ErrNotFound
	}
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE api_key_hash = ? AND disabled = 0`, hash))
}

// UsersByOrg lists accounts in an org.
func (s *Store) UsersByOrg(orgID int64) ([]*User, error) {
	rows, err := s.db.Query(`SELECT `+userCols+` FROM users WHERE org_id = ? ORDER BY username`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreateUser inserts a user, returning a friendly error on duplicates.
func (s *Store) CreateUser(u *User) error {
	res, err := s.db.Exec(`INSERT INTO users (org_id, username, email, password_hash, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, u.OrgID, u.Username, u.Email, u.PasswordHash, u.Role, now(), now())
	if err != nil {
		if isUnique(err) {
			return ErrConflict
		}
		return err
	}
	u.ID, _ = res.LastInsertId()
	return nil
}

// UpdateUser writes mutable profile fields.
func (s *Store) UpdateUser(u *User) error {
	_, err := s.db.Exec(`UPDATE users SET email = ?, role = ?, disabled = ?, updated_at = ? WHERE id = ?`,
		u.Email, u.Role, u.Disabled, now(), u.ID)
	return err
}

// SetPassword replaces the password hash.
func (s *Store) SetPassword(userID int64, hash string) error {
	_, err := s.db.Exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, now(), userID)
	return err
}

// SetAPIKey stores a new key hash, or clears it when hash is empty.
func (s *Store) SetAPIKey(userID int64, hash string) error {
	_, err := s.db.Exec(`UPDATE users SET api_key_hash = ?, updated_at = ? WHERE id = ?`, hash, now(), userID)
	return err
}

// SetTOTP stores a TOTP secret and whether it is confirmed.
func (s *Store) SetTOTP(userID int64, secret string, enabled bool) error {
	_, err := s.db.Exec(`UPDATE users SET totp_secret = ?, totp_enabled = ?, updated_at = ? WHERE id = ?`,
		secret, enabled, now(), userID)
	return err
}

// DeleteUser removes an account and its sessions.
func (s *Store) DeleteUser(id int64) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

// CountAdmins guards against removing the last administrator.
func (s *Store) CountAdmins(orgID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE org_id = ? AND role = 'admin' AND disabled = 0`, orgID).Scan(&n)
	return n, err
}

// CreateSession stores a hashed session token.
func (s *Store) CreateSession(tokenHash string, userID int64, ttl time.Duration, ip, ua string) error {
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at, created_at, ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?)`, tokenHash, userID, time.Now().Add(ttl).Unix(), now(), ip, ua)
	return err
}

// UserBySession resolves a session token hash to its user.
func (s *Store) UserBySession(tokenHash string) (*User, error) {
	var userID int64
	err := s.db.QueryRow(`SELECT user_id FROM sessions WHERE token_hash = ? AND expires_at > ?`, tokenHash, now()).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u, err := s.UserByID(userID)
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, ErrNotFound
	}
	return u, nil
}

// DeleteSession logs a session out.
func (s *Store) DeleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// PurgeSessions deletes expired sessions.
func (s *Store) PurgeSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, now())
	return err
}

// Org is a tenant boundary.
type Org struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	CreatedAt Time   `json:"created_at"`
}

// Org returns the tenant record.
func (s *Store) Org(id int64) (*Org, error) {
	var o Org
	var created int64
	err := s.db.QueryRow(`SELECT id, name, created_at FROM orgs WHERE id = ?`, id).Scan(&o.ID, &o.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	o.CreatedAt = unixTime(created)
	return &o, nil
}

// RenameOrg updates the tenant display name.
func (s *Store) RenameOrg(id int64, name string) error {
	_, err := s.db.Exec(`UPDATE orgs SET name = ? WHERE id = ?`, name, id)
	return err
}

func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "Duplicate entry")
}
