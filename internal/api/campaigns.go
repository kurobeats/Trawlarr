package api

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kurobeats/trawlarr/internal/notify"
	"github.com/kurobeats/trawlarr/internal/store"
)

func (s *Server) handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.Store.Campaigns(me.OrgID, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleGetCampaign(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := s.Store.Campaign(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// campaignPayload is the writable campaign shape.
type campaignPayload struct {
	Name        string  `json:"name"`
	TemplateID  int64   `json:"template_id"`
	PageID      int64   `json:"page_id"`
	ProfileID   int64   `json:"profile_id"`
	GroupIDs    []int64 `json:"group_ids"`
	URL         string  `json:"url"`
	ProxyURL    string  `json:"proxy_url"`
	CaptureCookies bool  `json:"capture_cookies"`
	LaunchAt    *store.Time `json:"launch_at"`
	SendBy      *store.Time `json:"send_by"`
	SendRate    int     `json:"send_rate"`
	Jitter      int     `json:"jitter_seconds"`
	ResendFailed bool   `json:"resend_failed"`
}

func (s *Server) handleCreateCampaign(w http.ResponseWriter, r *http.Request) { s.saveCampaign(w, r, 0) }

func (s *Server) handleUpdateCampaign(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	existing, err := s.Store.Campaign(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if existing.Status == store.StatusSending || existing.Status == store.StatusQueued {
		writeError(w, http.StatusConflict, "stop the campaign before editing it")
		return
	}
	s.saveCampaign(w, r, id)
}

func (s *Server) saveCampaign(w http.ResponseWriter, r *http.Request, id int64) {
	me := UserFrom(r)
	var req campaignPayload
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "campaign name is required")
		return
	}
	if len(req.GroupIDs) == 0 {
		writeError(w, http.StatusBadRequest, "select at least one recipient group")
		return
	}
	if req.ResendFailed == false {
		req.ResendFailed = false
	}
	c := &store.Campaign{
		OrgID:     me.OrgID,
		Name:      req.Name,
		TemplateID: req.TemplateID,
		PageID:    req.PageID,
		ProfileID: req.ProfileID,
		GroupIDs:  req.GroupIDs,
		URL:       strings.TrimSuffix(req.URL, "/"),
		ProxyURL:  strings.TrimSuffix(strings.TrimSpace(req.ProxyURL), "/"),
		CaptureCookies: req.CaptureCookies,
		SendRate:  req.SendRate,
		Jitter:    req.Jitter,
		ResendFailed: req.ResendFailed,
		Status:    store.StatusDraft,
		CreatedBy: me.ID,
	}
	if req.LaunchAt != nil {
		c.LaunchAt = *req.LaunchAt
	}
	if req.SendBy != nil {
		c.SendBy = *req.SendBy
	}
	if c.ProxyURL != "" {
		if err := s.validateProxyURL(c.ProxyURL); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if c.SendRate < 0 || c.Jitter < 0 {
		writeError(w, http.StatusBadRequest, "rate and jitter cannot be negative")
		return
	}
	if err := s.Store.SaveCampaign(c, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "campaign.saved", c.Name, "", s.clientIP(r))
	out, err := s.Store.Campaign(me.OrgID, c.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if id == 0 {
		writeJSON(w, http.StatusCreated, out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteCampaign(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.Engine.Stop(id)
	if err := s.Store.DeleteCampaign(me.OrgID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "campaign.deleted", strconv.FormatInt(id, 10), "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLaunchCampaign(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := s.Store.Campaign(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if c.Status == store.StatusQueued || c.Status == store.StatusSending {
		writeError(w, http.StatusConflict, "campaign is already running")
		return
	}
	if err := s.Engine.Launch(c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "campaign.launched", c.Name, "", s.clientIP(r))
	out, err := s.Store.Campaign(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStopCampaign(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := s.Store.Campaign(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.Engine.Stop(id)
	s.Store.Audit(me.OrgID, me.ID, "campaign.stopped", c.Name, "", s.clientIP(r))
	out, err := s.Store.Campaign(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleResendCampaign(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := s.Store.Campaign(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	n, err := s.Store.ResetFailed(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if n == 0 {
		writeError(w, http.StatusBadRequest, "no failed recipients to resend")
		return
	}
	if err := s.Store.SetCampaignStatus(id, store.StatusQueued); err != nil {
		writeStoreError(w, err)
		return
	}
	c.Status = store.StatusQueued
	if err := s.Engine.Launch(c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "campaign.resent", c.Name, strconv.FormatInt(n, 10)+" recipients", s.clientIP(r))
	out, _ := s.Store.Campaign(me.OrgID, id)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleExportCampaign(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := s.Store.Campaign(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(safeFilename(c.Name)+"-results.csv"))
	cw := csv.NewWriter(w)
	defer cw.Flush()
	cw.Write([]string{"email", "first_name", "last_name", "position", "status", "sent_at", "opened_at", "clicked_at", "submitted_at", "reported_at", "error"})
	for _, t := range c.Targets {
		cw.Write([]string{
			t.Email, t.FirstName, t.LastName, t.Position, t.Status,
			csvTime(t.SentAt), csvTime(t.OpenedAt), csvTime(t.ClickedAt),
			csvTime(t.SubmittedAt), csvTime(t.ReportedAt), t.Error,
		})
	}
}

func csvTime(t store.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Time().Format(time.RFC3339)
}

func safeFilename(s string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, s)
	if out == "" {
		return "campaign"
	}
	return out
}

// handleDashboard returns the home page summary in one round trip.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	campaigns, err := s.Store.Campaigns(me.OrgID, 100)
	if err != nil {
		writeStoreError(w, err)
		return
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
		if c.Status == store.StatusSending || c.Status == store.StatusQueued || c.Status == store.StatusScheduled {
			active++
		}
	}
	groups, _ := s.Store.Groups(me.OrgID)
	templates, _ := s.Store.Templates(me.OrgID)
	pages, _ := s.Store.Pages(me.OrgID)
	profiles, _ := s.Store.Profiles(me.OrgID)

	recent := campaigns
	if len(recent) > 5 {
		recent = recent[:5]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stats":          total,
		"active":         active,
		"campaign_count": len(campaigns),
		"object_counts": map[string]int{
			"groups":    len(groups),
			"templates": len(templates),
			"pages":     len(pages),
			"profiles":  len(profiles),
		},
		"recent": recent,
	})
}

// validateProxyURL enforces the opt-in and the allowlist.
func (s *Server) validateProxyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("relay URL must be a valid URL")
	}
	if u.Scheme != "https" {
		return errors.New("relay URL must use https")
	}
	if !s.Cfg.ProxyEnabled {
		return errors.New("relay campaigns are disabled; set proxy_enabled in config.json first")
	}
	if !s.Cfg.ProxyHostAllowed(u.Hostname()) {
		return fmt.Errorf("host %q is not in proxy_allowlist", u.Hostname())
	}
	return nil
}

// handleTargetSession returns the captured cookies for one recipient.
func (s *Server) handleTargetSession(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	campaignID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid campaign")
		return
	}
	targetID, err := strconv.ParseInt(r.PathValue("tid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid target")
		return
	}
	if _, err := s.Store.Campaign(me.OrgID, campaignID); err != nil {
		writeStoreError(w, err)
		return
	}
	upstream, cookies, err := s.Store.ProxySessionForTarget(campaignID, targetID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"upstream": upstream, "cookies": cookies})
}

// emitEvent lets handlers push ad-hoc events (currently unused by tracking,
// which writes through the store directly).
func (s *Server) emitEvent(orgID int64, p notify.Payload) { s.Hub.Emit(orgID, p) }
