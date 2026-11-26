// Package gophish imports data from an existing gophish database.
//
// It reads tables with SELECT * and looks columns up by name, so databases
// created by any gophish release from 0.5 onwards import cleanly.
package gophish

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/kurobeats/trawlarr/internal/store"
)

// Options configures an import.
type Options struct {
	Source string // sqlite file path or mysql://user:pass@host:port/dbname
	Dest   *store.Store
	Log    *slog.Logger
	Force  bool // import into a non-empty database
}

// Report summarises what was imported.
type Report struct {
	Users     int
	Groups    int
	Targets   int
	Templates int
	Attachments int
	Pages     int
	Profiles  int
	Campaigns int
	Results   int
	Events    int
	Webhooks  int
	Warnings  []string
}

// Import copies a gophish database into an empty Trawlarr database.
func Import(ctx context.Context, opts Options) (*Report, error) {
	rep := &Report{}
	orgCount, err := opts.Dest.OrgCount()
	if err != nil {
		return nil, err
	}
	if orgCount > 0 && !opts.Force {
		return nil, errors.New("destination database already has data; use -force to import anyway")
	}
	src, err := openSource(ctx, opts.Source)
	if err != nil {
		return nil, err
	}
	defer src.Close()

	orgID, err := ensureOrg(opts, rep)
	if err != nil {
		return nil, err
	}

	tables, err := sourceTables(ctx, src)
	if err != nil {
		return nil, err
	}
	if has(tables, "campaigns") == false && has(tables, "targets") == false {
		return nil, errors.New("source does not look like a gophish database")
	}

	roleSlugs := map[int64]string{}
	if has(tables, "roles") {
		if rows, err := readRows(ctx, src, `SELECT * FROM roles`); err == nil {
			for _, r := range rows {
				roleSlugs[i64(r, "id")] = str(r, "slug")
			}
		}
	}

	// Users -----------------------------------------------------------------
	userIDs := map[int64]int64{}
	if has(tables, "users") {
		rows, err := readRows(ctx, src, `SELECT * FROM users`)
		if err != nil {
			return nil, fmt.Errorf("read users: %w", err)
		}
		for _, r := range rows {
			role := store.RoleOperator
			if s := roleSlugs[i64(r, "role_id")]; s == "admin" {
				role = store.RoleAdmin
			} else if strings.EqualFold(str(r, "username"), "admin") {
				role = store.RoleAdmin
			}
			disabled := false
			if v, ok := r["account_locked"]; ok {
				disabled = truthy(v)
				if disabled {
					rep.Warnings = append(rep.Warnings, "user "+str(r, "username")+" was locked in gophish and imported as disabled")
				}
			}
			var newID int64
			err := opts.Dest.Tx(func(tx *sql.Tx) error {
				res, err := tx.Exec(`INSERT INTO users (org_id, username, email, password_hash, role, api_key_hash, disabled, created_at, updated_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					orgID, str(r, "username"), str(r, "email"), str(r, "hash"), role,
					store.HashToken(str(r, "api_key")), disabled, time.Now().Unix(), time.Now().Unix())
				if err != nil {
					return err
				}
				newID, err = res.LastInsertId()
				return err
			})
			if err != nil {
				if errors.Is(err, store.ErrConflict) || strings.Contains(err.Error(), "UNIQUE") {
					rep.Warnings = append(rep.Warnings, "skipped duplicate user "+str(r, "username"))
					continue
				}
				return nil, fmt.Errorf("insert user %q: %w", str(r, "username"), err)
			}
			userIDs[i64(r, "id")] = newID
			rep.Users++
		}
		if rep.Users > 0 {
			rep.Warnings = append(rep.Warnings, "API keys were imported as hashes; users must regenerate theirs under Settings")
		}
	}

	// Groups and targets ----------------------------------------------------
	groupIDs := map[int64]int64{}
	groupTargets := map[int64][]int64{}
	if has(tables, "group_targets") {
		if rows, err := readRows(ctx, src, `SELECT * FROM group_targets`); err == nil {
			for _, r := range rows {
				groupTargets[i64(r, "group_id")] = append(groupTargets[i64(r, "group_id")], i64(r, "target_id"))
			}
		}
	}
	targetByID := map[int64]map[string]any{}
	if has(tables, "targets") {
		rows, err := readRows(ctx, src, `SELECT * FROM targets`)
		if err != nil {
			return nil, fmt.Errorf("read targets: %w", err)
		}
		for _, r := range rows {
			targetByID[i64(r, "id")] = r
		}
	}
	if has(tables, "groups") {
		rows, err := readRows(ctx, src, `SELECT * FROM groups`)
		if err != nil {
			return nil, fmt.Errorf("read groups: %w", err)
		}
		for _, g := range rows {
			oldID := i64(g, "id")
			err := opts.Dest.Tx(func(tx *sql.Tx) error {
				res, err := tx.Exec(`INSERT INTO target_groups (org_id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
					orgID, str(g, "name"), unixOf(g, "modified_date"), time.Now().Unix())
				if err != nil {
					return err
				}
				newID, err := res.LastInsertId()
				if err != nil {
					return err
				}
				groupIDs[oldID] = newID
				seen := map[string]bool{}
				for _, tid := range groupTargets[oldID] {
					t, ok := targetByID[tid]
					if !ok {
						continue
					}
					email := str(t, "email")
					if email == "" || seen[strings.ToLower(email)] {
						continue
					}
					seen[strings.ToLower(email)] = true
					if _, err := tx.Exec(`INSERT INTO targets (group_id, email, first_name, last_name, position, custom)
						VALUES (?, ?, ?, ?, ?, '{}')`, newID, email, str(t, "first_name"), str(t, "last_name"), str(t, "position")); err != nil {
						return err
					}
					rep.Targets++
				}
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("import group %q: %w", str(g, "name"), err)
			}
			rep.Groups++
		}
	}

	// Attachments and templates --------------------------------------------
	attachByTemplate := map[int64][]int64{}
	if has(tables, "attachments") {
		rows, err := readRows(ctx, src, `SELECT * FROM attachments`)
		if err != nil {
			return nil, fmt.Errorf("read attachments: %w", err)
		}
		for _, a := range rows {
			content, err := base64.StdEncoding.DecodeString(str(a, "content"))
			if err != nil {
				rep.Warnings = append(rep.Warnings, "attachment "+str(a, "name")+" was not valid base64 and was skipped")
				continue
			}
			var newID int64
			err = opts.Dest.Tx(func(tx *sql.Tx) error {
				res, err := tx.Exec(`INSERT INTO attachments (org_id, name, content_type, content, size, created_at)
					VALUES (?, ?, ?, ?, ?, ?)`, orgID, str(a, "name"), nonEmpty(str(a, "type"), "application/octet-stream"),
					content, len(content), time.Now().Unix())
				if err != nil {
					return err
				}
				newID, err = res.LastInsertId()
				return err
			})
			if err != nil {
				return nil, err
			}
			attachByTemplate[i64(a, "template_id")] = append(attachByTemplate[i64(a, "template_id")], newID)
			rep.Attachments++
		}
	}

	templateIDs := map[int64]int64{}
	if has(tables, "templates") {
		rows, err := readRows(ctx, src, `SELECT * FROM templates`)
		if err != nil {
			return nil, fmt.Errorf("read templates: %w", err)
		}
		for _, t := range rows {
			oldID := i64(t, "id")
			attachments := attachByTemplate[oldID]
			if attachments == nil {
				attachments = []int64{}
			}
			var newID int64
			err := opts.Dest.Tx(func(tx *sql.Tx) error {
				res, err := tx.Exec(`INSERT INTO templates (org_id, name, subject, text_body, html_body, attachments, created_at, updated_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, orgID, str(t, "name"), str(t, "subject"), str(t, "text"),
					str(t, "html"), mustJSONInts(attachments), unixOf(t, "modified_date"), time.Now().Unix())
				if err != nil {
					return err
				}
				newID, err = res.LastInsertId()
				return err
			})
			if err != nil {
				return nil, fmt.Errorf("import template %q: %w", str(t, "name"), err)
			}
			templateIDs[oldID] = newID
			rep.Templates++
		}
	}

	pageIDs := map[int64]int64{}
	if has(tables, "pages") {
		rows, err := readRows(ctx, src, `SELECT * FROM pages`)
		if err != nil {
			return nil, fmt.Errorf("read pages: %w", err)
		}
		for _, p := range rows {
			oldID := i64(p, "id")
			var newID int64
			err := opts.Dest.Tx(func(tx *sql.Tx) error {
				res, err := tx.Exec(`INSERT INTO pages (org_id, name, html, capture, capture_passwords, redirect_url, created_at, updated_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, orgID, str(p, "name"), str(p, "html"),
					truthy(p["capture_credentials"]), truthy(p["capture_passwords"]), str(p, "redirect_url"),
					unixOf(p, "modified_date"), time.Now().Unix())
				if err != nil {
					return err
				}
				newID, err = res.LastInsertId()
				return err
			})
			if err != nil {
				return nil, fmt.Errorf("import page %q: %w", str(p, "name"), err)
			}
			pageIDs[oldID] = newID
			rep.Pages++
		}
	}

	// Sending profiles -----------------------------------------------------
	profileIDs := map[int64]int64{}
	if has(tables, "smtp") {
		headers := map[int64][]store.Header{}
		if has(tables, "headers") {
			if hrows, err := readRows(ctx, src, `SELECT * FROM headers`); err == nil {
				for _, h := range hrows {
					headers[i64(h, "smtp_id")] = append(headers[i64(h, "smtp_id")],
						store.Header{Key: str(h, "key"), Value: str(h, "value")})
				}
			}
		}
		rows, err := readRows(ctx, src, `SELECT * FROM smtp`)
		if err != nil {
			return nil, fmt.Errorf("read sending profiles: %w", err)
		}
		for _, p := range rows {
			oldID := i64(p, "id")
			host, port := splitHost(str(p, "host"))
			enc, err := opts.Dest.Seal(str(p, "password"))
			if err != nil {
				return nil, err
			}
			var newID int64
			err = opts.Dest.Tx(func(tx *sql.Tx) error {
				res, err := tx.Exec(`INSERT INTO profiles (org_id, name, host, port, username, password, from_address,
					envelope_sender, ignore_cert, headers, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					orgID, str(p, "name"), host, port, str(p, "username"), enc, str(p, "from_address"),
					"", truthy(p["ignore_cert_errors"]), mustJSONHeaders(headers[oldID]),
					unixOf(p, "modified_date"), time.Now().Unix())
				if err != nil {
					return err
				}
				newID, err = res.LastInsertId()
				return err
			})
			if err != nil {
				return nil, fmt.Errorf("import profile %q: %w", str(p, "name"), err)
			}
			profileIDs[oldID] = newID
			rep.Profiles++
		}
	}

	// Campaigns ------------------------------------------------------------
	campaignGroups := map[int64][]int64{}
	if has(tables, "campaign_groups") {
		if rows, err := readRows(ctx, src, `SELECT * FROM campaign_groups`); err == nil {
			for _, r := range rows {
				if gid, ok := groupIDs[i64(r, "group_id")]; ok {
					campaignGroups[i64(r, "campaign_id")] = append(campaignGroups[i64(r, "campaign_id")], gid)
				}
			}
		}
	}
	campaignIDs := map[int64]int64{}
	if has(tables, "campaigns") {
		rows, err := readRows(ctx, src, `SELECT * FROM campaigns`)
		if err != nil {
			return nil, fmt.Errorf("read campaigns: %w", err)
		}
		previouslySending := 0
		for _, c := range rows {
			oldID := i64(c, "id")
			status := mapCampaignStatus(str(c, "status"))
			if status == store.StatusSending {
				status = store.StatusStopped
				previouslySending++
			}
			var newID int64
			err := opts.Dest.Tx(func(tx *sql.Tx) error {
				res, err := tx.Exec(`INSERT INTO campaigns (org_id, name, template_id, page_id, profile_id, group_ids, url,
					status, launch_at, send_by, created_by, created_at, updated_at, started_at, completed_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					orgID, str(c, "name"), templateIDs[i64(c, "template_id")], pageIDs[i64(c, "page_id")],
					profileIDs[i64(c, "smtp_id")], mustJSONInts(campaignGroups[oldID]), str(c, "url"), status,
					unixOf(c, "launch_date"), unixOf(c, "send_by_date"), 0,
					unixOf(c, "created_date"), time.Now().Unix(), unixOf(c, "launch_date"), unixOf(c, "completed_date"))
				if err != nil {
					return err
				}
				newID, err = res.LastInsertId()
				return err
			})
			if err != nil {
				return nil, fmt.Errorf("import campaign %q: %w", str(c, "name"), err)
			}
			campaignIDs[oldID] = newID
			rep.Campaigns++
		}
		if previouslySending > 0 {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d campaign(s) were mid-send and were imported as stopped; relaunch when ready", previouslySending))
		}
	}

	// Events give us the milestone timestamps, so read them before results.
	type milestone struct {
		sent, opened, clicked, submitted, reported bool
	}
	milestones := map[string]*milestone{}
	if has(tables, "events") {
		rows, err := readRows(ctx, src, `SELECT * FROM events`)
		if err != nil {
			return nil, fmt.Errorf("read events: %w", err)
		}
		// gophish orders events by insert; keep the first occurrence of each type.
		for _, e := range rows {
			cid := campaignIDs[i64(e, "campaign_id")]
			if cid == 0 {
				continue
			}
			key := campaignKey(cid, str(e, "email"))
			m, ok := milestones[key]
			if !ok {
				m = &milestone{}
				milestones[key] = m
			}
			switch str(e, "message") {
			case "Email Sent":
				m.sent = true
			case "Email Opened":
				m.opened = true
			case "Clicked Link":
				m.clicked = true
			case "Submitted Data":
				m.submitted = true
			case "Email Reported":
				m.reported = true
			}
			if err := opts.Dest.Tx(func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO events (campaign_id, rid, type, message, at) VALUES (?, '', ?, ?, ?)`,
					cid, mapEventType(str(e, "message")), str(e, "message"), unixOf(e, "time"))
				return err
			}); err != nil {
				return nil, err
			}
			rep.Events++
		}
	}

	// Results --------------------------------------------------------------
	if has(tables, "results") {
		rows, err := readRows(ctx, src, `SELECT * FROM results`)
		if err != nil {
			return nil, fmt.Errorf("read results: %w", err)
		}
		for _, r := range rows {
			cid := campaignIDs[i64(r, "campaign_id")]
			if cid == 0 {
				continue
			}
			rid := nonEmpty(str(r, "r_id"), store.NewRID())
			status := mapResultStatus(str(r, "status"))
			m := milestones[campaignKey(cid, str(r, "email"))]
			sentAt, openedAt, clickedAt, submittedAt, reportedAt := int64(0), int64(0), int64(0), int64(0), int64(0)
			if m != nil {
				if m.sent {
					sentAt = unixOf(r, "send_date")
				}
				if m.opened {
					openedAt = unixOf(r, "modified_date")
				}
				if m.clicked {
					clickedAt = unixOf(r, "modified_date")
				}
				if m.submitted {
					submittedAt = unixOf(r, "modified_date")
				}
				if m.reported {
					reportedAt = unixOf(r, "modified_date")
				}
			}
			err := opts.Dest.Tx(func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO campaign_targets (campaign_id, rid, email, first_name, last_name, position,
					status, sent_at, opened_at, clicked_at, submitted_at, reported_at, ip, updated_at)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, cid, rid, str(r, "email"),
					str(r, "first_name"), str(r, "last_name"), str(r, "position"), status,
					sentAt, openedAt, clickedAt, submittedAt, reportedAt, str(r, "ip"), time.Now().Unix())
				if err != nil {
					if strings.Contains(err.Error(), "UNIQUE") {
						return nil // duplicate rid from a copied campaign
					}
					return err
				}
				rep.Results++
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}

	// Webhooks -------------------------------------------------------------
	if has(tables, "webhooks") {
		rows, err := readRows(ctx, src, `SELECT * FROM webhooks`)
		if err != nil {
			return nil, fmt.Errorf("read webhooks: %w", err)
		}
		for _, w := range rows {
			err := opts.Dest.Tx(func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO webhooks (org_id, name, url, secret, events, enabled, created_at, updated_at)
					VALUES (?, ?, ?, ?, ? , ?, ?, ?)`, orgID, str(w, "name"), str(w, "url"), str(w, "secret"),
					`["*"]`, truthy(w["is_active"]), time.Now().Unix(), time.Now().Unix())
				return err
			})
			if err != nil {
				return nil, err
			}
			rep.Webhooks++
		}
	}

	// IMAP reporting settings ----------------------------------------------
	if has(tables, "imap") {
		if rows, err := readRows(ctx, src, `SELECT * FROM imap LIMIT 1`); err == nil && len(rows) > 0 {
			r := rows[0]
			enc, _ := opts.Dest.Seal(str(r, "password"))
			for k, v := range map[string]string{
				"imap_host": str(r, "host"), "imap_port": strconv.FormatInt(i64(r, "port"), 10),
				"imap_username": str(r, "username"), "imap_password": enc,
				"imap_folder": str(r, "folder"), "imap_enabled": strconv.FormatBool(truthy(r["enabled"])),
				"imap_tls": strconv.FormatBool(truthy(r["tls"])),
				"imap_ignore_cert": strconv.FormatBool(truthy(r["ignore_cert_errors"])),
			} {
				if v != "" {
					_ = opts.Dest.SetSetting(k, v)
				}
			}
			rep.Warnings = append(rep.Warnings, "IMAP settings imported; enable them in Settings once the report mailbox credentials are confirmed")
		}
	}

	rep.Warnings = append(rep.Warnings, "imported campaigns are never auto-launched")
	return rep, nil
}

func ensureOrg(opts Options, rep *Report) (int64, error) {
	// Reuse the existing org when importing into a fresh-but-seeded database.
	var id int64
	err := opts.Dest.Tx(func(tx *sql.Tx) error {
		err := tx.QueryRow(`SELECT id FROM orgs ORDER BY id LIMIT 1`).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			res, err := tx.Exec(`INSERT INTO orgs (name, created_at) VALUES ('Imported', ?)`, time.Now().Unix())
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		}
		return err
	})
	return id, err
}

func openSource(ctx context.Context, source string) (*sql.DB, error) {
	if source == "" {
		return nil, errors.New("-from is required")
	}
	if strings.HasPrefix(source, "mysql://") {
		u, err := url.Parse(source)
		if err != nil {
			return nil, err
		}
		dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=false&timeout=10s",
			u.User.Username(), password(u), u.Host, strings.TrimPrefix(u.Path, "/"))
		return sql.Open("mysql", dsn)
	}
	path := source
	if !strings.HasPrefix(path, "file:") {
		path = "file:" + path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return sql.Open("sqlite", path+sep+"mode=ro&_pragma=busy_timeout(5000)")
}

func password(u *url.URL) string {
	if p, ok := u.User.Password(); ok {
		return p
	}
	return ""
}

// sourceTables lists the tables so version differences never block an import.
func sourceTables(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		// MySQL fallback
		rows, err = db.QueryContext(ctx, `SHOW TABLES`)
		if err != nil {
			return nil, err
		}
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[strings.ToLower(name)] = true
	}
	return out, rows.Err()
}

func has(tables map[string]bool, name string) bool { return tables[name] }

// readRows returns each row as a column->value map.
func readRows(ctx context.Context, db *sql.DB, query string) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			switch v := vals[i].(type) {
			case []byte:
				m[strings.ToLower(c)] = string(v)
			default:
				m[strings.ToLower(c)] = v
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func str(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return fmt.Sprint(v)
}

func i64(m map[string]any, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case []byte:
		n, _ := strconv.ParseInt(string(t), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case int64:
		return t != 0
	case float64:
		return t != 0
	case []byte:
		return truthy(string(t))
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "1" || s == "true" || s == "yes"
	}
	return false
}

// unixOf parses a gophish datetime column, which varies by release.
func unixOf(m map[string]any, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	var t store.Time
	if err := t.Scan(v); err != nil {
		return 0
	}
	if t.IsZero() {
		return 0
	}
	return t.Time().Unix()
}

func splitHost(raw string) (string, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 587
	}
	if i := strings.LastIndex(raw, ":"); i > 0 {
		port, err := strconv.Atoi(raw[i+1:])
		if err == nil {
			return raw[:i], port
		}
	}
	return raw, 587
}

func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func campaignKey(campaignID int64, email string) string {
	return strconv.FormatInt(campaignID, 10) + "|" + strings.ToLower(email)
}

func mapCampaignStatus(s string) string {
	switch s {
	case "Completed", "Emails Sent":
		return store.StatusCompleted
	case "In progress":
		return store.StatusSending
	case "Scheduled":
		return store.StatusScheduled
	case "Stopped":
		return store.StatusStopped
	case "Error":
		return store.StatusError
	default:
		return store.StatusDraft
	}
}

func mapResultStatus(s string) string {
	switch s {
	case "Email Sent":
		return "sent"
	case "Email Opened":
		return "opened"
	case "Clicked Link":
		return "clicked"
	case "Submitted Data":
		return "submitted"
	case "Email Reported":
		return "reported"
	case "Error", "Error Sending Email":
		return "failed"
	default:
		return "sent"
	}
}

func mapEventType(message string) string {
	switch message {
	case "Email Sent":
		return store.EvEmailSent
	case "Error Sending Email":
		return store.EvEmailFailed
	case "Email Opened":
		return store.EvEmailOpened
	case "Clicked Link":
		return store.EvLinkClicked
	case "Submitted Data":
		return store.EvDataSubmitted
	case "Email Reported":
		return store.EvEmailReported
	case "Campaign Completed":
		return store.EvCampaignCompleted
	case "Campaign Stopped":
		return store.EvCampaignStopped
	default:
		return store.EvCampaignStarted
	}
}

func mustJSONInts(ids []int64) string {
	if ids == nil {
		ids = []int64{}
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

func mustJSONHeaders(hs []store.Header) string {
	if hs == nil {
		hs = []store.Header{}
	}
	b, _ := json.Marshal(hs)
	return string(b)
}
