package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kurobeats/trawlarr/internal/secret"
)

// Common errors returned by the store.
var (
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("already exists")
	ErrForbidden = errors.New("forbidden")
)

// Store is the persistence layer. All timestamps are stored as unix seconds.
type Store struct {
	db  *sql.DB
	box *secret.Box
}

// New wraps a database handle.
func New(db *sql.DB, box *secret.Box) *Store {
	return &Store{db: db, box: box}
}

// Vector renders a copy of the captured form data for API callers.
type Vector map[string]any

// HashToken returns the hex sha256 of a bearer token, which is all we keep.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func now() int64 { return time.Now().UTC().Unix() }

// nullInt reads a nullable integer column.
func nullInt(v sql.NullInt64) int64 {
	if v.Valid {
		return v.Int64
	}
	return 0
}

// jsonInts marshals a list of ids for storage in a TEXT column.
func jsonInts(ids []int64) string {
	if ids == nil {
		ids = []int64{}
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

func parseInts(s string) []int64 {
	out := []int64{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func jsonStrings(ss []string) string {
	if ss == nil {
		ss = []string{}
	}
	b, _ := json.Marshal(ss)
	return string(b)
}

func parseStrings(s string) []string {
	out := []string{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// Setting reads a key from the settings table.
func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting writes a key to the settings table.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Audit appends an entry to the audit log.
func (s *Store) Audit(orgID, userID int64, action, target, detail, ip string) {
	_, err := s.db.Exec(`INSERT INTO audit_log (org_id, user_id, action, target, detail, ip, at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, orgID, userID, action, target, detail, ip, now())
	_ = err // auditing must never break the request
}

// AuditEntry is a row from the audit log.
type AuditEntry struct {
	ID       int64  `json:"id"`
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	Detail   string `json:"detail"`
	IP       string `json:"ip"`
	At       Time   `json:"at"`
}

// AuditLog lists the most recent audit entries for an org.
func (s *Store) AuditLog(orgID int64, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT a.id, a.user_id, COALESCE(u.username, ''), a.action, a.target, a.detail, a.ip, a.at
		FROM audit_log a LEFT JOIN users u ON u.id = a.user_id
		WHERE a.org_id = ? ORDER BY a.id DESC LIMIT ?`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var at int64
		if err := rows.Scan(&e.ID, &e.UserID, &e.Username, &e.Action, &e.Target, &e.Detail, &e.IP, &at); err != nil {
			return nil, err
		}
		e.At = unixTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Seed creates the default org and admin user on a fresh database.
func (s *Store) Seed(username, passwordHash string) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO orgs (name, created_at) VALUES ('Default', ?)`, now())
	if err != nil {
		return false, err
	}
	orgID, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO users (org_id, username, email, password_hash, role, created_at, updated_at)
		VALUES (?, ?, '', ?, 'admin', ?, ?)`, orgID, username, passwordHash, now(), now()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// OrgCount returns the number of orgs, used by import tooling.
func (s *Store) OrgCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM orgs`).Scan(&n)
	return n, err
}

// deleteScoped removes a row that belongs to an org and reports a miss as
// ErrNotFound, so callers never claim to have deleted someone else's record.
func (s *Store) deleteScoped(table string, orgID, id int64) error {
	res, err := s.db.Exec(`DELETE FROM `+table+` WHERE id = ? AND org_id = ?`, id, orgID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MustJSON is a tiny helper for building detail strings.
func MustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// DB exposes the handle for migrations and tests. Prefer the typed methods.
func (s *Store) DB() *sql.DB { return s.db }

// Tx runs fn inside a single transaction.
func (s *Store) Tx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// DecryptSetting opens a value stored by Seal, tolerating plaintext.
func (s *Store) DecryptSetting(value string) (string, error) {
	plain, err := s.box.Open(value)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// Seal exposes encryption for migration tooling.
func (s *Store) Seal(v string) (string, error) { return s.box.Seal([]byte(v)) }
