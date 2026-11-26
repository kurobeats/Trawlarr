package store

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ParseTargetsCSV reads a recipient list. A header row is optional; without one
// the columns are first_name,last_name,email,position. Unknown columns become
// custom fields. This is the single parser behind both the HTTP import and the
// MCP tool.
func ParseTargetsCSV(rd io.Reader) ([]Target, error) {
	raw, err := io.ReadAll(rd)
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	reader := csv.NewReader(bufio.NewReader(bytes.NewReader(raw)))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("invalid CSV: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("CSV is empty")
	}
	header := map[string]int{}
	start := 0
	first := strings.ToLower(strings.Join(records[0], ","))
	if strings.Contains(first, "email") && !strings.Contains(first, "@") {
		for i, col := range records[0] {
			norm := strings.NewReplacer(" ", "_", "-", "_").Replace(strings.ToLower(strings.TrimSpace(col)))
			header[norm] = i
		}
		start = 1
	} else {
		header = map[string]int{"first_name": 0, "last_name": 1, "email": 2, "position": 3}
		// No header: find the email column by looking at the values, then lay
		// the remaining columns out as first/last/position. Handles exports
		// that put email first.
		if emailCol := sniffEmailColumn(records[0]); emailCol >= 0 {
			rest := []int{}
			for i := range records[0] {
				if i != emailCol {
					rest = append(rest, i)
				}
			}
			header = map[string]int{"email": emailCol}
			names := []string{"first_name", "last_name", "position"}
			for i, col := range rest {
				if i < len(names) {
					header[names[i]] = col
				}
			}
		}
	}
	get := func(row []string, key string) string {
		i, ok := header[key]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	known := map[string]bool{}
	for _, k := range []string{"first_name", "last_name", "email", "position", "firstname", "lastname"} {
		known[k] = true
	}
	var out []Target
	seen := map[string]bool{}
	for _, row := range records[start:] {
		email := get(row, "email")
		if email == "" || !strings.Contains(email, "@") {
			continue
		}
		key := strings.ToLower(email)
		if seen[key] {
			continue
		}
		seen[key] = true
		t := Target{
			Email:     email,
			FirstName: firstNonEmpty(get(row, "first_name"), get(row, "firstname")),
			LastName:  firstNonEmpty(get(row, "last_name"), get(row, "lastname")),
			Position:  get(row, "position"),
			Custom:    map[string]string{},
		}
		for col, i := range header {
			if known[col] || i >= len(row) {
				continue
			}
			if v := strings.TrimSpace(row[i]); v != "" {
				t.Custom[col] = v
			}
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, errors.New("no valid recipients found (need an email column)")
	}
	return out, nil
}

// sniffEmailColumn returns the index holding an address, or -1.
func sniffEmailColumn(row []string) int {
	for i, v := range row {
		v = strings.TrimSpace(v)
		if strings.Contains(v, "@") && !strings.ContainsAny(v, " ,") {
			return i
		}
	}
	return -1
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
