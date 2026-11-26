package store

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Time is a UTC timestamp that persists as unix seconds and marshals as
// RFC3339 (or null when zero). It removes timezone guesswork: the database
// only ever holds UTC, and the browser renders local time.
type Time time.Time

// TimeOf wraps a time.Time.
func TimeOf(t time.Time) Time { return Time(t.UTC()) }

// unixTime converts a stored unix second count, treating 0 as "never".
func unixTime(n int64) Time {
	if n == 0 {
		return Time(time.Time{})
	}
	return Time(time.Unix(n, 0).UTC())
}

// Time returns the underlying time.Time in UTC.
func (t Time) Time() time.Time { return time.Time(t).UTC() }

// IsZero reports whether the timestamp is unset.
func (t Time) IsZero() bool { return time.Time(t).IsZero() }

// MarshalJSON emits RFC3339 or null.
func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return []byte(strconv.Quote(t.Time().Format(time.RFC3339))), nil
}

// UnmarshalJSON accepts RFC3339, null, or the empty string.
func (t *Time) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" || s == `""` {
		*t = Time(time.Time{})
		return nil
	}
	var v string
	if err := jsonUnquote(s, &v); err != nil {
		return err
	}
	return t.parse(v)
}

func jsonUnquote(s string, out *string) error {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return fmt.Errorf("store: invalid time value %s", s)
	}
	*out = s[1 : len(s)-1]
	return nil
}

func (t *Time) parse(v string) error {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04:05-07:00", "2006-01-02"} {
		if parsed, err := time.Parse(layout, v); err == nil {
			*t = Time(parsed.UTC())
			return nil
		}
	}
	return fmt.Errorf("store: unparseable time %q", v)
}

// Scan implements sql.Scanner.
func (t *Time) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*t = Time(time.Time{})
	case time.Time:
		*t = Time(v.UTC())
	case int64:
		if v == 0 {
			*t = Time(time.Time{})
		} else {
			*t = unixTime(v)
		}
	case []byte:
		return t.scanString(string(v))
	case string:
		return t.scanString(v)
	default:
		return fmt.Errorf("store: cannot scan %T into Time", src)
	}
	return nil
}

func (t *Time) scanString(s string) error {
	if s == "" || s == "0" {
		*t = Time(time.Time{})
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*t = unixTime(n)
		return nil
	}
	return t.parse(s)
}

// Value implements driver.Valuer.
func (t Time) Value() (driver.Value, error) {
	if t.IsZero() {
		return int64(0), nil
	}
	return t.Time().Unix(), nil
}

// ErrNoRows re-exports sql.ErrNoRows so callers need one error import.
var ErrNoRows = errors.New("sql: no rows in result set")
