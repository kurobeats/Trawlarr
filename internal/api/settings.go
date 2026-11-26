package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kurobeats/trawlarr/internal/ai"
	"github.com/kurobeats/trawlarr/internal/config"
	"github.com/kurobeats/trawlarr/internal/store"
)

// Settings that are safe to change while the server is running live in the
// settings table so the UI can edit them. Values that need a restart to take
// effect (listen addresses, database path, encryption key) and the relay
// allowlist stay in config.json on purpose.

type imapSettings struct {
	Enabled         bool   `json:"enabled"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	Password        string `json:"password,omitempty"`
	HasPassword     bool   `json:"has_password"`
	ClearPassword   bool   `json:"clear_password,omitempty"`
	UseTLS          bool   `json:"use_tls"`
	IgnoreCert      bool   `json:"ignore_cert"`
	Folder          string `json:"folder"`
	RestrictDomain  string `json:"restrict_domain"`
	DeleteReported  bool   `json:"delete_reported"`
	IntervalMinutes int    `json:"interval_minutes"`
}

type mcpSettings struct {
	Enabled     bool   `json:"enabled"`
	AllowWrites bool   `json:"allow_writes"`
	User        string `json:"user"`
}

type aiSettings struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"base_url"`
	Model          string `json:"model"`
	APIKey         string `json:"api_key,omitempty"`
	HasAPIKey      bool   `json:"has_api_key"`
	ClearAPIKey    bool   `json:"clear_api_key,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	org, _ := s.Store.Org(me.OrgID)
	out := map[string]any{
		"phish_url": s.phishBaseURL(),
		"admin_url": s.adminBaseURL(),
		"events":    store.AllEvents,
		"version":   Version,
		"imap":      s.imapSettings(),
		"ai":        s.aiSettings(),
		"mcp":       s.mcpSettings(),
	}
	if org != nil {
		out["org_name"] = org.Name
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpdateSettings applies whichever sections are present in the body.
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	var req struct {
		OrgName        *string       `json:"org_name"`
		PhishPublicURL *string       `json:"phish_public_url"`
		AdminPublicURL *string       `json:"admin_public_url"`
		IMAP           *imapSettings `json:"imap"`
		AI             *aiSettings   `json:"ai"`
		MCP            *mcpSettings  `json:"mcp"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	changed := []string{}
	restartReasons := []string{}
	if req.OrgName != nil {
		name := strings.TrimSpace(*req.OrgName)
		if name == "" {
			writeError(w, http.StatusBadRequest, "organisation name cannot be empty")
			return
		}
		if err := s.Store.RenameOrg(me.OrgID, name); err != nil {
			writeStoreError(w, err)
			return
		}
		changed = append(changed, "org_name")
	}
	if req.AdminPublicURL != nil {
		raw := strings.TrimSpace(*req.AdminPublicURL)
		if raw != "" {
			u, err := url.Parse(raw)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				writeError(w, http.StatusBadRequest, "admin URL must be http(s)")
				return
			}
		}
		if err := s.Store.SetSetting("admin_public_url", strings.TrimSuffix(raw, "/")); err != nil {
			writeStoreError(w, err)
			return
		}
		changed = append(changed, "admin_public_url")
		// Cookies are scoped to the host, so a changed admin address means
		// signing in again; a restart makes the switch unambiguous.
		restartReasons = append(restartReasons, "admin URL")
	}
	if req.PhishPublicURL != nil {
		raw := strings.TrimSpace(*req.PhishPublicURL)
		if raw != "" {
			u, err := url.Parse(raw)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				writeError(w, http.StatusBadRequest, "tracking base URL must be http(s)")
				return
			}
		}
		if err := s.Store.SetSetting("phish_public_url", strings.TrimSuffix(raw, "/")); err != nil {
			writeStoreError(w, err)
			return
		}
		changed = append(changed, "phish_public_url")
	}
	if req.IMAP != nil {
		if err := s.saveIMAPSettings(req.IMAP); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		changed = append(changed, "imap")
	}
	if req.MCP != nil {
		if err := s.saveMCPSettings(me.OrgID, req.MCP); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		changed = append(changed, "mcp")
	}
	if req.AI != nil {
		if err := s.saveAISettings(req.AI); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.reloadAI()
		changed = append(changed, "ai")
	}

	s.Store.Audit(me.OrgID, me.ID, "settings.updated", strings.Join(changed, ","), "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"restart_required": len(restartReasons) > 0,
		"restart_reasons":  restartReasons,
	})
}

// ------------------------------------------------------------------- IMAP

func (s *Server) imapSettings() imapSettings {
	cur := s.Cfg.IMAP
	if s.Store == nil {
		return imapSettings{Host: cur.Host, Port: cur.Port, Username: cur.Username, Folder: cur.Folder,
			UseTLS: cur.UseTLS, IgnoreCert: cur.IgnoreCert, RestrictDomain: cur.RestrictDomain,
			DeleteReported: cur.DeleteReported, IntervalMinutes: cur.IntervalMinutes}
	}
	out := imapSettings{
		Enabled:         cur.Host != "",
		Host:            cur.Host,
		Port:            cur.Port,
		Username:        cur.Username,
		HasPassword:     false,
		UseTLS:          cur.UseTLS,
		IgnoreCert:      cur.IgnoreCert,
		Folder:          cur.Folder,
		RestrictDomain:  cur.RestrictDomain,
		DeleteReported:  cur.DeleteReported,
		IntervalMinutes: cur.IntervalMinutes,
	}
	if enc, _ := s.Store.Setting("imap_password"); enc != "" {
		out.HasPassword = true
	}
	str := func(key string, dst *string) {
		if v, _ := s.Store.Setting(key); v != "" {
			*dst = v
		}
	}
	str("imap_host", &out.Host)
	str("imap_username", &out.Username)
	str("imap_folder", &out.Folder)
	str("imap_restrict_domain", &out.RestrictDomain)
	if v, _ := s.Store.Setting("imap_port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			out.Port = n
		}
	}
	if v, _ := s.Store.Setting("imap_interval_minutes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			out.IntervalMinutes = n
		}
	}
	for key, dst := range map[string]*bool{
		"imap_enabled":         &out.Enabled,
		"imap_use_tls":         &out.UseTLS,
		"imap_ignore_cert":     &out.IgnoreCert,
		"imap_delete_reported": &out.DeleteReported,
	} {
		if v, _ := s.Store.Setting(key); v != "" {
			*dst = v == "true" || v == "1"
		}
	}
	if out.Port == 0 {
		out.Port = 993
	}
	if out.Folder == "" {
		out.Folder = "INBOX"
	}
	if out.IntervalMinutes == 0 {
		out.IntervalMinutes = 5
	}
	return out
}

func (s *Server) saveIMAPSettings(in *imapSettings) error {
	if in.Port != 0 && (in.Port < 1 || in.Port > 65535) {
		return errors.New("IMAP port must be between 1 and 65535")
	}
	if in.IntervalMinutes != 0 && (in.IntervalMinutes < 1 || in.IntervalMinutes > 1440) {
		return errors.New("poll interval must be between 1 and 1440 minutes")
	}
	host := strings.TrimSpace(in.Host)
	if in.Enabled && host == "" {
		return errors.New("IMAP host is required when monitoring is enabled")
	}
	if in.Enabled && strings.TrimSpace(in.Username) == "" {
		return errors.New("IMAP username is required when monitoring is enabled")
	}
	port := in.Port
	if port == 0 {
		port = 993
	}
	folder := strings.TrimSpace(in.Folder)
	if folder == "" {
		folder = "INBOX"
	}
	interval := in.IntervalMinutes
	if interval == 0 {
		interval = 5
	}
	pairs := map[string]string{
		"imap_host":             host,
		"imap_port":             strconv.Itoa(port),
		"imap_username":         strings.TrimSpace(in.Username),
		"imap_folder":           folder,
		"imap_restrict_domain":  strings.TrimSpace(in.RestrictDomain),
		"imap_interval_minutes": strconv.Itoa(interval),
		"imap_enabled":          strconv.FormatBool(in.Enabled),
		"imap_use_tls":          strconv.FormatBool(in.UseTLS),
		"imap_ignore_cert":      strconv.FormatBool(in.IgnoreCert),
		"imap_delete_reported":  strconv.FormatBool(in.DeleteReported),
	}
	for k, v := range pairs {
		if err := s.Store.SetSetting(k, v); err != nil {
			return err
		}
	}
	switch {
	case in.ClearPassword:
		if err := s.Store.SetSetting("imap_password", ""); err != nil {
			return err
		}
	case in.Password != "":
		sealed, err := s.Store.Seal(in.Password)
		if err != nil {
			return err
		}
		if err := s.Store.SetSetting("imap_password", sealed); err != nil {
			return err
		}
	}
	return nil
}

// -------------------------------------------------------------------- MCP

func (s *Server) mcpSettings() mcpSettings {
	cur := s.Cfg.MCPFrom(s.Store)
	return mcpSettings{Enabled: cur.Enabled, AllowWrites: cur.AllowWrites, User: cur.User}
}

func (s *Server) saveMCPSettings(orgID int64, in *mcpSettings) error {
	user := strings.TrimSpace(in.User)
	if user != "" {
		u, err := s.Store.UserByUsername(user)
		if err != nil || u.OrgID != orgID {
			return errors.New("MCP user does not exist")
		}
		if u.Disabled {
			return errors.New("MCP user is disabled")
		}
		if store.Rank(u.Role) < store.Rank(store.RoleViewer) {
			return errors.New("MCP user needs a valid role")
		}
	}
	pairs := map[string]string{
		"mcp_enabled":      strconv.FormatBool(in.Enabled),
		"mcp_allow_writes": strconv.FormatBool(in.AllowWrites),
		"mcp_user":         user,
	}
	for k, v := range pairs {
		if err := s.Store.SetSetting(k, v); err != nil {
			return err
		}
	}
	return nil
}

// --------------------------------------------------------------------- AI

func (s *Server) aiSettings() aiSettings {
	cur := s.Cfg.AI
	if s.Store == nil {
		return aiSettings{Enabled: cur.Enabled, BaseURL: cur.BaseURL, Model: cur.Model, TimeoutSeconds: cur.TimeoutSeconds}
	}
	out := aiSettings{
		Enabled:        cur.Enabled,
		BaseURL:        cur.BaseURL,
		Model:          cur.Model,
		TimeoutSeconds: cur.TimeoutSeconds,
	}
	if enc, _ := s.Store.Setting("ai_api_key"); enc != "" {
		out.HasAPIKey = true
	}
	if v, _ := s.Store.Setting("ai_enabled"); v != "" {
		out.Enabled = v == "true" || v == "1"
	}
	if v, _ := s.Store.Setting("ai_base_url"); v != "" {
		out.BaseURL = v
	}
	if v, _ := s.Store.Setting("ai_model"); v != "" {
		out.Model = v
	}
	if v, _ := s.Store.Setting("ai_timeout_seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			out.TimeoutSeconds = n
		}
	}
	return out
}

func (s *Server) saveAISettings(in *aiSettings) error {
	base := strings.TrimSpace(in.BaseURL)
	if base != "" {
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("AI base URL must be http(s)")
		}
	}
	model := strings.TrimSpace(in.Model)
	if in.Enabled && (base == "" || model == "") {
		return errors.New("AI base URL and model are required when enabled")
	}
	if in.TimeoutSeconds != 0 && (in.TimeoutSeconds < 5 || in.TimeoutSeconds > 600) {
		return errors.New("AI timeout must be between 5 and 600 seconds")
	}
	timeout := in.TimeoutSeconds
	if timeout == 0 {
		timeout = 45
	}
	pairs := map[string]string{
		"ai_enabled":         strconv.FormatBool(in.Enabled),
		"ai_base_url":        base,
		"ai_model":           model,
		"ai_timeout_seconds": strconv.Itoa(timeout),
	}
	for k, v := range pairs {
		if err := s.Store.SetSetting(k, v); err != nil {
			return err
		}
	}
	switch {
	case in.ClearAPIKey:
		if err := s.Store.SetSetting("ai_api_key", ""); err != nil {
			return err
		}
	case in.APIKey != "":
		sealed, err := s.Store.Seal(in.APIKey)
		if err != nil {
			return err
		}
		if err := s.Store.SetSetting("ai_api_key", sealed); err != nil {
			return err
		}
	}
	return nil
}

// effectiveAIConfig merges stored settings over the config file.
func (s *Server) effectiveAIConfig() config.AI {
	stored := s.aiSettings()
	out := config.AI{
		Enabled:        stored.Enabled,
		BaseURL:        stored.BaseURL,
		Model:          stored.Model,
		TimeoutSeconds: stored.TimeoutSeconds,
	}
	if enc, _ := s.Store.Setting("ai_api_key"); enc != "" {
		if plain, err := s.Store.DecryptSetting(enc); err == nil {
			out.APIKey = plain
		}
	} else {
		out.APIKey = s.Cfg.AI.APIKey
	}
	return out
}

// reloadAI rebuilds the client so a settings change takes effect immediately.
func (s *Server) reloadAI() {
	if s.Store == nil {
		if s.Cfg != nil {
			s.AI = ai.New(s.Cfg.AI)
		}
		return
	}
	s.AI = ai.New(s.effectiveAIConfig())
}

// adminBaseURL is the address operators reach the admin UI on, when it differs
// from what the server sees (reverse proxies, port forwards).
func (s *Server) adminBaseURL() string {
	if v, _ := s.Store.Setting("admin_public_url"); v != "" {
		return v
	}
	return s.Cfg.AdminServer.BaseURL()
}

// phishBaseURL is the origin used in tracking links: the UI override wins over
// the config file.
func (s *Server) phishBaseURL() string {
	if v, _ := s.Store.Setting("phish_public_url"); v != "" {
		return v
	}
	return s.Cfg.PhishServer.BaseURL()
}
