package mcp

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	mcp_golang "github.com/metoro-io/mcp-golang"

	"github.com/kurobeats/trawlarr/internal/ai"
	"github.com/kurobeats/trawlarr/internal/store"
)

// argument types ------------------------------------------------------------

type limitArgs struct {
	Limit int `json:"limit" jsonschema:"description=Maximum rows to return (default 50, max 200)"`
}

type idArgs struct {
	ID int64 `json:"id" jsonschema:"required,description=Campaign, template or page id"`
}

type campaignDetailArgs struct {
	ID            int64 `json:"id" jsonschema:"required,description=Campaign id"`
	Recipients    bool  `json:"include_recipients" jsonschema:"description=Include one row per recipient"`
	TimelineLimit int   `json:"timeline_limit" jsonschema:"description=Number of timeline events (default 25)"`
}

type listGroupsArgs struct {
	IncludeTargets bool `json:"include_targets" jsonschema:"description=Include every recipient in each group"`
}

type draftArgs struct {
	Prompt   string `json:"prompt" jsonschema:"required,description=What the content should say"`
	Tone     string `json:"tone" jsonschema:"description=e.g. urgent, neutral, friendly"`
	Audience string `json:"audience" jsonschema:"description=Who receives it, e.g. finance team"`
}

type createGroupArgs struct {
	Name string `json:"name" jsonschema:"required,description=Group name"`
	CSV  string `json:"csv" jsonschema:"required,description=CSV with email,first_name,last_name,position (header optional)"`
}

type createTemplateArgs struct {
	Name     string `json:"name" jsonschema:"required,description=Template name"`
	Subject  string `json:"subject" jsonschema:"required,description=Subject line, may use {{.FirstName}}"`
	TextBody string `json:"text_body" jsonschema:"description=Plain text body"`
	HTMLBody string `json:"html_body" jsonschema:"description=HTML body; include {{.URL}} and {{.Tracker}}"`
}

type createPageArgs struct {
	Name             string `json:"name" jsonschema:"required,description=Page name"`
	HTML             string `json:"html" jsonschema:"required,description=Full HTML document"`
	Capture          bool   `json:"capture" jsonschema:"description=Record submitted field names"`
	CaptureValues    bool   `json:"capture_values" jsonschema:"description=Also store what was typed (off by default)"`
	CapturePasswords bool   `json:"capture_passwords" jsonschema:"description=Include password fields in captured data"`
	RedirectURL      string `json:"redirect_url" jsonschema:"description=Where to send the recipient after submitting"`
}

type createCampaignArgs struct {
	Name          string  `json:"name" jsonschema:"required,description=Campaign name"`
	TemplateID    int64   `json:"template_id" jsonschema:"required,description=Email template id"`
	PageID        int64   `json:"page_id" jsonschema:"required,description=Landing page id"`
	ProfileID     int64   `json:"profile_id" jsonschema:"required,description=Sending profile id"`
	GroupIDs      []int64 `json:"group_ids" jsonschema:"required,description=Recipient group ids"`
	URL           string  `json:"url" jsonschema:"description=Tracking URL base; defaults to the configured phishing server"`
	SendRate      int     `json:"send_rate" jsonschema:"description=Emails per hour, 0 for unrestricted"`
	JitterSeconds int     `json:"jitter_seconds" jsonschema:"description=Random extra delay between emails"`
	LaunchAt      string  `json:"launch_at" jsonschema:"description=RFC3339 time to start; empty means start when launched"`
}

// read-only tools -----------------------------------------------------------

func (s *Server) dashboard(limitArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.guard(); err != nil {
		return nil, err
	}
	campaigns, err := s.store.Campaigns(s.actorUser().OrgID, 200)
	if err != nil {
		return nil, err
	}
	var total store.Stats
	active := 0
	for _, c := range campaigns {
		total.Total += c.Stats.Total
		total.Sent += c.Stats.Sent
		total.Opened += c.Stats.Opened
		total.Clicked += c.Stats.Clicked
		total.Submitted += c.Stats.Submitted
		total.Reported += c.Stats.Reported
		total.Failed += c.Stats.Failed
		total.Uncertain += c.Stats.Uncertain
		if c.Status == store.StatusSending || c.Status == store.StatusQueued || c.Status == store.StatusScheduled {
			active++
		}
	}
	groups, _ := s.store.Groups(s.actorUser().OrgID)
	templates, _ := s.store.Templates(s.actorUser().OrgID)
	pages, _ := s.store.Pages(s.actorUser().OrgID)
	profiles, _ := s.store.Profiles(s.actorUser().OrgID)
	return jsonResponse(map[string]any{
		"campaigns": len(campaigns),
		"active":    active,
		"stats":     total,
		"library": map[string]int{
			"groups": len(groups), "templates": len(templates),
			"landing_pages": len(pages), "sending_profiles": len(profiles),
		},
	})
}

type campaignRow struct {
	ID        int64       `json:"id"`
	Name      string      `json:"name"`
	Status    string      `json:"status"`
	CreatedAt any         `json:"created_at"`
	Stats     store.Stats `json:"stats"`
}

func (s *Server) listCampaigns(args limitArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.guard(); err != nil {
		return nil, err
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 50
	}
	campaigns, err := s.store.Campaigns(s.actorUser().OrgID, limit)
	if err != nil {
		return nil, err
	}
	rows := make([]campaignRow, 0, len(campaigns))
	for _, c := range campaigns {
		rows = append(rows, campaignRow{ID: c.ID, Name: c.Name, Status: c.Status, CreatedAt: c.CreatedAt, Stats: c.Stats})
	}
	return jsonResponse(map[string]any{"campaigns": rows, "count": len(rows)})
}

func (s *Server) getCampaign(args campaignDetailArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.guard(); err != nil {
		return nil, err
	}
	c, err := s.store.Campaign(s.actorUser().OrgID, args.ID)
	if err != nil {
		return nil, fmt.Errorf("campaign %d: %w", args.ID, err)
	}
	out := map[string]any{
		"id": c.ID, "name": c.Name, "status": c.Status, "stats": c.Stats,
		"template_id": c.TemplateID, "page_id": c.PageID, "profile_id": c.ProfileID,
		"group_ids": c.GroupIDs, "url": c.URL, "proxy_url": c.ProxyURL,
		"send_rate": c.SendRate, "jitter_seconds": c.Jitter,
		"created_at": c.CreatedAt, "started_at": c.StartedAt, "completed_at": c.CompletedAt,
	}
	if args.Recipients {
		type recipient struct {
			Email     string   `json:"email"`
			Name      string   `json:"name"`
			Status    string   `json:"status"`
			Error     string   `json:"error,omitempty"`
			Opened    any      `json:"opened_at"`
			Clicked   any      `json:"clicked_at"`
			Submitted any      `json:"submitted_at"`
			Reported  any      `json:"reported_at"`
			Fields    []string `json:"fields,omitempty"`
			Data      any      `json:"data,omitempty"`
			Session   bool     `json:"has_session,omitempty"`
		}
		rows := make([]recipient, 0, len(c.Targets))
		for _, t := range c.Targets {
			rows = append(rows, recipient{
				Email: t.Email, Name: strings.TrimSpace(t.FirstName + " " + t.LastName), Status: t.Status,
				Error: t.Error, Opened: t.OpenedAt, Clicked: t.ClickedAt, Submitted: t.SubmittedAt,
				Reported: t.ReportedAt, Fields: t.Fields, Data: t.Data, Session: t.HasSession,
			})
		}
		out["recipients"] = rows
	}
	limit := args.TimelineLimit
	if limit <= 0 || limit > 200 {
		limit = 25
	}
	events := make([]map[string]any, 0, len(c.Timeline))
	for i, e := range c.Timeline {
		if i >= limit {
			break
		}
		events = append(events, map[string]any{
			"type": e.Type, "message": e.Message, "email": e.Email, "at": e.At, "ip": e.IP,
		})
	}
	out["timeline"] = events
	return jsonResponse(out)
}

func (s *Server) exportCampaign(args idArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.guard(); err != nil {
		return nil, err
	}
	c, err := s.store.Campaign(s.actorUser().OrgID, args.ID)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"email", "first_name", "last_name", "position", "status", "sent_at", "opened_at", "clicked_at", "submitted_at", "reported_at", "error"})
	for _, t := range c.Targets {
		_ = w.Write([]string{
			t.Email, t.FirstName, t.LastName, t.Position, t.Status,
			csvTime(t.SentAt), csvTime(t.OpenedAt), csvTime(t.ClickedAt),
			csvTime(t.SubmittedAt), csvTime(t.ReportedAt), t.Error,
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	s.audit("export_campaign", strconv.FormatInt(c.ID, 10), "")
	return mcp_golang.NewToolResponse(mcp_golang.NewTextContent(buf.String())), nil
}

func csvTime(t store.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Time().Format(time.RFC3339)
}

func (s *Server) analyzeCampaign(args idArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.guard(); err != nil {
		return nil, err
	}
	if s.ai == nil {
		return nil, errors.New("AI analysis is disabled; set ai.enabled, ai.base_url and ai.model in config.json")
	}
	c, err := s.store.Campaign(s.actorUser().OrgID, args.ID)
	if err != nil {
		return nil, err
	}
	// Only aggregates leave the process: no addresses, no captured values.
	text, err := s.ai.Analyze(context.Background(), ai.Summary{
		Name: c.Name, Status: c.Status,
		Recipients: c.Stats.Total, Sent: c.Stats.Sent,
		Opened: c.Stats.Opened, Clicked: c.Stats.Clicked,
		Submitted: c.Stats.Submitted, Reported: c.Stats.Reported,
		Failed: c.Stats.Failed, Uncertain: c.Stats.Uncertain,
		SendRate: c.SendRate,
		Duration: durationOf(c.StartedAt, c.CompletedAt),
	})
	if err != nil {
		return nil, err
	}
	s.audit("analyze_campaign", strconv.FormatInt(c.ID, 10), "")
	return mcp_golang.NewToolResponse(mcp_golang.NewTextContent(text)), nil
}

func durationOf(start, end store.Time) string {
	if start.IsZero() || end.IsZero() {
		return "unknown"
	}
	d := end.Time().Sub(start.Time())
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}

func (s *Server) listGroups(args listGroupsArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.guard(); err != nil {
		return nil, err
	}
	groups, err := s.store.Groups(s.actorUser().OrgID)
	if err != nil {
		return nil, err
	}
	type row struct {
		ID      int64          `json:"id"`
		Name    string         `json:"name"`
		Size    int            `json:"size"`
		Targets []store.Target `json:"targets,omitempty"`
	}
	rows := make([]row, 0, len(groups))
	for _, g := range groups {
		r := row{ID: g.ID, Name: g.Name, Size: len(g.Targets)}
		if args.IncludeTargets {
			r.Targets = g.Targets
		}
		rows = append(rows, r)
	}
	return jsonResponse(map[string]any{"groups": rows, "count": len(rows)})
}

func (s *Server) listTemplates(limitArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.guard(); err != nil {
		return nil, err
	}
	templates, err := s.store.Templates(s.actorUser().OrgID)
	if err != nil {
		return nil, err
	}
	type row struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Subject string `json:"subject"`
	}
	rows := make([]row, 0, len(templates))
	for _, t := range templates {
		rows = append(rows, row{ID: t.ID, Name: t.Name, Subject: t.Subject})
	}
	return jsonResponse(map[string]any{"templates": rows, "count": len(rows)})
}

func (s *Server) getTemplate(args idArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.guard(); err != nil {
		return nil, err
	}
	t, err := s.store.Template(s.actorUser().OrgID, args.ID)
	if err != nil {
		return nil, err
	}
	return jsonResponse(map[string]any{
		"id": t.ID, "name": t.Name, "subject": t.Subject,
		"text_body": t.TextBody, "html_body": t.HTMLBody,
		"attachments": t.Attachments,
	})
}

func (s *Server) listPages(limitArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.guard(); err != nil {
		return nil, err
	}
	pages, err := s.store.Pages(s.actorUser().OrgID)
	if err != nil {
		return nil, err
	}
	type row struct {
		ID               int64  `json:"id"`
		Name             string `json:"name"`
		Capture          bool   `json:"capture"`
		CaptureValues    bool   `json:"capture_values"`
		CapturePasswords bool   `json:"capture_passwords"`
		RedirectURL      string `json:"redirect_url"`
	}
	rows := make([]row, 0, len(pages))
	for _, p := range pages {
		rows = append(rows, row{ID: p.ID, Name: p.Name, Capture: p.Capture,
			CaptureValues: p.CaptureValues, CapturePasswords: p.CapturePasswords, RedirectURL: p.RedirectURL})
	}
	return jsonResponse(map[string]any{"landing_pages": rows, "count": len(rows)})
}

func (s *Server) listProfiles(limitArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.guard(); err != nil {
		return nil, err
	}
	profiles, err := s.store.Profiles(s.actorUser().OrgID)
	if err != nil {
		return nil, err
	}
	type row struct {
		ID            int64  `json:"id"`
		Name          string `json:"name"`
		Host          string `json:"host"`
		Port          int    `json:"port"`
		FromAddress   string `json:"from_address"`
		LastTestError string `json:"last_test_error,omitempty"`
		Tested        bool   `json:"tested"`
	}
	rows := make([]row, 0, len(profiles))
	for _, p := range profiles {
		rows = append(rows, row{ID: p.ID, Name: p.Name, Host: p.Host, Port: p.Port,
			FromAddress: p.FromAddress, LastTestError: p.LastTestError, Tested: !p.LastTestAt.IsZero()})
	}
	return jsonResponse(map[string]any{"sending_profiles": rows, "count": len(rows)})
}

func (s *Server) draftEmail(args draftArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.guard(); err != nil {
		return nil, err
	}
	if s.ai == nil {
		return nil, errors.New("AI drafting is disabled; set ai.enabled, ai.base_url and ai.model in config.json")
	}
	draft, err := s.ai.DraftEmail(context.Background(), ai.Request{
		Prompt: args.Prompt, Tone: args.Tone, Audience: args.Audience,
	})
	if err != nil {
		return nil, err
	}
	s.audit("draft_email", "", "")
	return jsonResponse(draft)
}

func (s *Server) draftLandingPage(args draftArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.guard(); err != nil {
		return nil, err
	}
	if s.ai == nil {
		return nil, errors.New("AI drafting is disabled; set ai.enabled, ai.base_url and ai.model in config.json")
	}
	draft, err := s.ai.DraftPage(context.Background(), ai.Request{
		Prompt: args.Prompt, Tone: args.Tone, Audience: args.Audience,
	})
	if err != nil {
		return nil, err
	}
	s.audit("draft_landing_page", "", "")
	return jsonResponse(draft)
}

// write tools ---------------------------------------------------------------

func (s *Server) createGroup(args createGroupArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.requireWrite("create_group"); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	targets, err := store.ParseTargetsCSV(strings.NewReader(args.CSV))
	if err != nil {
		return nil, err
	}
	g := &store.Group{OrgID: s.actorUser().OrgID, Name: name, Targets: targets}
	if err := s.store.SaveGroup(g, 0); err != nil {
		return nil, err
	}
	s.audit("create_group", g.Name, strconv.Itoa(len(targets))+" recipients")
	return jsonResponse(map[string]any{"id": g.ID, "name": g.Name, "recipients": len(targets)})
}

func (s *Server) createTemplate(args createTemplateArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.requireWrite("create_template"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Name) == "" {
		return nil, errors.New("name is required")
	}
	if args.HTMLBody == "" && args.TextBody == "" {
		return nil, errors.New("a template needs a text or html body")
	}
	t := &store.Template{OrgID: s.actorUser().OrgID, Name: args.Name, Subject: args.Subject,
		TextBody: args.TextBody, HTMLBody: args.HTMLBody}
	if err := s.store.SaveTemplate(t, 0); err != nil {
		return nil, err
	}
	s.audit("create_template", t.Name, "")
	return jsonResponse(map[string]any{"id": t.ID, "name": t.Name})
}

func (s *Server) createLandingPage(args createPageArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.requireWrite("create_landing_page"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Name) == "" || strings.TrimSpace(args.HTML) == "" {
		return nil, errors.New("name and html are required")
	}
	// Recording values is a privacy decision, not a default.
	p := &store.Page{OrgID: s.actorUser().OrgID, Name: args.Name, HTML: args.HTML,
		Capture: args.Capture || args.CaptureValues, CaptureValues: args.CaptureValues,
		CapturePasswords: args.CapturePasswords, RedirectURL: args.RedirectURL}
	if err := s.store.SavePage(p, 0); err != nil {
		return nil, err
	}
	s.audit("create_landing_page", p.Name, "")
	return jsonResponse(map[string]any{"id": p.ID, "name": p.Name, "capture": p.Capture, "capture_values": p.CaptureValues})
}

func (s *Server) createCampaign(args createCampaignArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.requireWrite("create_campaign"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Name) == "" {
		return nil, errors.New("name is required")
	}
	if len(args.GroupIDs) == 0 {
		return nil, errors.New("at least one group is required")
	}
	// Validate references up front so a draft is never half-broken.
	if _, err := s.store.Template(s.actorUser().OrgID, args.TemplateID); err != nil {
		return nil, fmt.Errorf("template %d: %w", args.TemplateID, err)
	}
	if _, err := s.store.Page(s.actorUser().OrgID, args.PageID); err != nil {
		return nil, fmt.Errorf("landing page %d: %w", args.PageID, err)
	}
	if _, err := s.store.Profile(s.actorUser().OrgID, args.ProfileID); err != nil {
		return nil, fmt.Errorf("sending profile %d: %w", args.ProfileID, err)
	}
	for _, gid := range args.GroupIDs {
		if _, err := s.store.Group(s.actorUser().OrgID, gid); err != nil {
			return nil, fmt.Errorf("group %d: %w", gid, err)
		}
	}
	c := &store.Campaign{OrgID: s.actorUser().OrgID, Name: args.Name, TemplateID: args.TemplateID,
		PageID: args.PageID, ProfileID: args.ProfileID, GroupIDs: args.GroupIDs,
		URL: strings.TrimSuffix(args.URL, "/"), SendRate: args.SendRate, Jitter: args.JitterSeconds,
		Status: store.StatusDraft, CreatedBy: s.actorUser().ID}
	if args.LaunchAt != "" {
		when, err := time.Parse(time.RFC3339, args.LaunchAt)
		if err != nil {
			return nil, errors.New("launch_at must be RFC3339, for example 2026-01-02T09:00:00Z")
		}
		c.LaunchAt = store.TimeOf(when)
	}
	if err := s.store.SaveCampaign(c, 0); err != nil {
		return nil, err
	}
	s.audit("create_campaign", c.Name, "")
	return jsonResponse(map[string]any{"id": c.ID, "name": c.Name, "status": c.Status, "launch_at": c.LaunchAt})
}

func (s *Server) launchCampaign(args idArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.requireWrite("launch_campaign"); err != nil {
		return nil, err
	}
	c, err := s.store.Campaign(s.actorUser().OrgID, args.ID)
	if err != nil {
		return nil, err
	}
	if c.Status == store.StatusQueued || c.Status == store.StatusSending {
		return nil, errors.New("campaign is already running")
	}
	// Validate before queuing so the sending server never picks up a broken
	// campaign.
	if _, err := s.store.Template(s.actorUser().OrgID, c.TemplateID); err != nil {
		return nil, fmt.Errorf("template %d: %w", c.TemplateID, err)
	}
	if _, err := s.store.Page(s.actorUser().OrgID, c.PageID); err != nil {
		return nil, fmt.Errorf("landing page %d: %w", c.PageID, err)
	}
	if _, err := s.store.Profile(s.actorUser().OrgID, c.ProfileID); err != nil {
		return nil, fmt.Errorf("sending profile %d: %w", c.ProfileID, err)
	}
	if len(c.GroupIDs) == 0 {
		return nil, errors.New("campaign has no recipient groups")
	}
	if n, err := s.store.MaterializeTargets(c); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, errors.New("no recipients (all may have opted out)")
	}
	// The MCP process does not send mail itself: it queues the campaign and the
	// running Trawlarr server starts it within a couple of seconds. Writing the
	// queue entry here keeps that hand-off explicit in the audit log.
	now := time.Now().UTC()
	c.LaunchAt = store.TimeOf(now)
	if err := s.store.SaveCampaign(c, c.ID); err != nil {
		return nil, err
	}
	if err := s.store.SetCampaignStatus(c.ID, store.StatusQueued); err != nil {
		return nil, err
	}
	s.audit("launch_campaign", c.Name, "queued")
	return jsonResponse(map[string]any{"id": c.ID, "queued": true,
		"note": "the Trawlarr server will start sending within a few seconds"})
}

func (s *Server) stopCampaign(args idArgs) (*mcp_golang.ToolResponse, error) {
	if err := s.requireWrite("stop_campaign"); err != nil {
		return nil, err
	}
	c, err := s.store.Campaign(s.actorUser().OrgID, args.ID)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetCampaignStatus(c.ID, store.StatusStopped); err != nil {
		return nil, err
	}
	s.audit("stop_campaign", c.Name, "")
	return jsonResponse(map[string]any{"id": c.ID, "stopped": true})
}
