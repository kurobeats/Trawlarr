// Package api exposes the admin JSON API and the public phishing endpoints.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kurobeats/trawlarr/internal/ai"
	"github.com/kurobeats/trawlarr/internal/config"
	"github.com/kurobeats/trawlarr/internal/engine"
	"github.com/kurobeats/trawlarr/internal/mail"
	"github.com/kurobeats/trawlarr/internal/notify"
	"github.com/kurobeats/trawlarr/internal/store"
)

// SessionCookie is the admin session cookie name.
const SessionCookie = "trawlarr_session"

// Server holds dependencies shared by all handlers.
type Server struct {
	Store  *store.Store
	Engine *engine.Engine
	Sender mail.Sender
	Hub    *notify.Dispatcher
	Log    *slog.Logger
	Cfg    *config.Config
	UI     fs.FS
	AI     *ai.Client

	limiter  *limiter
	proxyJar *proxyJars
}

// New wires a Server.
func New(s *store.Store, e *engine.Engine, sender mail.Sender, hub *notify.Dispatcher, cfg *config.Config, log *slog.Logger, ui fs.FS) *Server {
	srv := &Server{Store: s, Engine: e, Sender: sender, Hub: hub, Cfg: cfg, Log: log, UI: ui,
		limiter: newLimiter(), proxyJar: newProxyJars()}
	if cfg != nil {
		srv.AI = ai.New(cfg.AI)
	}
	return srv
}

// routes registers admin endpoints.
func (s *Server) routes(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/login", s.public(s.handleLogin))
	mux.Handle("POST /api/v1/logout", s.authenticated(s.handleLogout))
	mux.Handle("GET /api/v1/me", s.authenticated(s.handleMe))
	mux.Handle("POST /api/v1/me/password", s.authenticated(s.handleChangePassword))
	mux.Handle("POST /api/v1/me/apikey", s.authenticated(s.handleRegenerateAPIKey))
	mux.Handle("POST /api/v1/me/totp/setup", s.authenticated(s.handleTOTPSetup))
	mux.Handle("POST /api/v1/me/totp/enable", s.authenticated(s.handleTOTPEnable))
	mux.Handle("POST /api/v1/me/totp/disable", s.authenticated(s.handleTOTPDisable))

	mux.Handle("GET /api/v1/ai/status", s.viewer(s.handleAIStatus))
	mux.Handle("POST /api/v1/ai/generate", s.operator(s.handleAIGenerate))

	mux.Handle("GET /api/v1/dashboard", s.viewer(s.handleDashboard))
	mux.Handle("GET /api/v1/audit", s.viewer(s.handleAudit))

	mux.Handle("GET /api/v1/groups", s.viewer(s.handleListGroups))
	mux.Handle("POST /api/v1/groups", s.operator(s.handleCreateGroup))
	mux.Handle("POST /api/v1/groups/import", s.operator(s.handleImportGroupCSV))
	mux.Handle("GET /api/v1/groups/{id}", s.viewer(s.handleGetGroup))
	mux.Handle("PUT /api/v1/groups/{id}", s.operator(s.handleUpdateGroup))
	mux.Handle("DELETE /api/v1/groups/{id}", s.operator(s.handleDeleteGroup))

	mux.Handle("GET /api/v1/templates", s.viewer(s.handleListTemplates))
	mux.Handle("POST /api/v1/templates", s.operator(s.handleCreateTemplate))
	mux.Handle("GET /api/v1/templates/{id}", s.viewer(s.handleGetTemplate))
	mux.Handle("PUT /api/v1/templates/{id}", s.operator(s.handleUpdateTemplate))
	mux.Handle("DELETE /api/v1/templates/{id}", s.operator(s.handleDeleteTemplate))

	mux.Handle("GET /api/v1/pages", s.viewer(s.handleListPages))
	mux.Handle("POST /api/v1/pages", s.operator(s.handleCreatePage))
	mux.Handle("GET /api/v1/pages/{id}", s.viewer(s.handleGetPage))
	mux.Handle("PUT /api/v1/pages/{id}", s.operator(s.handleUpdatePage))
	mux.Handle("DELETE /api/v1/pages/{id}", s.operator(s.handleDeletePage))
	mux.Handle("POST /api/v1/pages/import-url", s.operator(s.handleImportLandingPage))

	mux.Handle("GET /api/v1/profiles", s.viewer(s.handleListProfiles))
	mux.Handle("POST /api/v1/profiles", s.operator(s.handleCreateProfile))
	mux.Handle("GET /api/v1/profiles/{id}", s.viewer(s.handleGetProfile))
	mux.Handle("PUT /api/v1/profiles/{id}", s.operator(s.handleUpdateProfile))
	mux.Handle("DELETE /api/v1/profiles/{id}", s.operator(s.handleDeleteProfile))
	mux.Handle("POST /api/v1/profiles/{id}/test", s.operator(s.handleTestProfile))

	mux.Handle("GET /api/v1/attachments", s.viewer(s.handleListAttachments))
	mux.Handle("POST /api/v1/attachments", s.operator(s.handleUploadAttachment))
	mux.Handle("GET /api/v1/attachments/{id}/download", s.viewer(s.handleDownloadAttachment))
	mux.Handle("DELETE /api/v1/attachments/{id}", s.operator(s.handleDeleteAttachment))

	mux.Handle("GET /api/v1/webhooks", s.viewer(s.handleListWebhooks))
	mux.Handle("POST /api/v1/webhooks", s.operator(s.handleCreateWebhook))
	mux.Handle("PUT /api/v1/webhooks/{id}", s.operator(s.handleUpdateWebhook))
	mux.Handle("DELETE /api/v1/webhooks/{id}", s.operator(s.handleDeleteWebhook))

	mux.Handle("GET /api/v1/campaigns", s.viewer(s.handleListCampaigns))
	mux.Handle("POST /api/v1/campaigns", s.operator(s.handleCreateCampaign))
	mux.Handle("GET /api/v1/campaigns/{id}", s.viewer(s.handleGetCampaign))
	mux.Handle("PUT /api/v1/campaigns/{id}", s.operator(s.handleUpdateCampaign))
	mux.Handle("DELETE /api/v1/campaigns/{id}", s.operator(s.handleDeleteCampaign))
	mux.Handle("POST /api/v1/campaigns/{id}/launch", s.operator(s.handleLaunchCampaign))
	mux.Handle("POST /api/v1/campaigns/{id}/stop", s.operator(s.handleStopCampaign))
	mux.Handle("POST /api/v1/campaigns/{id}/resend", s.operator(s.handleResendCampaign))
	mux.Handle("GET /api/v1/campaigns/{id}/export", s.viewer(s.handleExportCampaign))
	mux.Handle("GET /api/v1/campaigns/{id}/targets/{tid}/session", s.viewer(s.handleTargetSession))
	mux.Handle("POST /api/v1/campaigns/{id}/analyze", s.operator(s.handleAnalyzeCampaign))

	mux.Handle("GET /api/v1/users", s.admin(s.handleListUsers))
	mux.Handle("POST /api/v1/users", s.admin(s.handleCreateUser))
	mux.Handle("PUT /api/v1/users/{id}", s.admin(s.handleUpdateUser))
	mux.Handle("DELETE /api/v1/users/{id}", s.admin(s.handleDeleteUser))

	mux.Handle("GET /api/v1/settings", s.viewer(s.handleGetSettings))
	mux.Handle("PUT /api/v1/settings", s.admin(s.handleUpdateSettings))

	mux.Handle("GET /api/v1/health", s.public(s.handleHealth))
}

// trackingRoutes registers the endpoints recipients touch. Each listener adds
// its own catch-all so admin and combined modes cannot collide.
func (s *Server) trackingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/px/", s.handleProxyRelay)
	mux.HandleFunc("GET /open.gif", s.handleOpen)
	mux.HandleFunc("GET /click", s.handleClick)
	mux.HandleFunc("POST /submit", s.handleSubmit)
	mux.HandleFunc("GET /report", s.handleReport)
	mux.HandleFunc("GET /optout", s.handleOptOut)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("User-agent: *\nDisallow: /\n"))
	})
}

// phishFallback answers everything else on the phishing listener. Old-style
// links land on "/" with ?rid=, so those are treated as clicks.
func (s *Server) phishFallback(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("rid") != "" && r.Method == http.MethodGet {
		s.handleClick(w, r)
		return
	}
	serveNotFound(w)
}

// AdminHandler serves the admin API plus the embedded UI.
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	mux.Handle("/", s.serveUI())
	return s.securityHeaders(mux)
}

// PhishHandler serves only tracking and landing endpoints.
func (s *Server) PhishHandler() http.Handler {
	mux := http.NewServeMux()
	s.trackingRoutes(mux)
	mux.HandleFunc("/", s.phishFallback)
	return mux
}

// CombinedHandler serves both audiences from one listener when the admin and
// phishing addresses match.
func (s *Server) CombinedHandler() http.Handler {
	mux := http.NewServeMux()
	s.trackingRoutes(mux)
	s.routes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("rid") != "" && r.Method == http.MethodGet {
			s.handleClick(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		s.serveUI().ServeHTTP(w, r)
	})
	return s.securityHeaders(mux)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// clientIP resolves the best available address for logging.
func (s *Server) clientIP(r *http.Request) string {
	if s.Cfg.TrustProxy {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			return strings.TrimSpace(strings.Split(fwd, ",")[0])
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// ---------------------------------------------------------------- middleware

type ctxKey string

const userKey ctxKey = "user"

// UserFrom returns the authenticated user for a request.
func UserFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

func (s *Server) public(h http.HandlerFunc) http.Handler {
	return s.originGuard(h)
}

func (s *Server) authenticated(h http.HandlerFunc) http.Handler {
	return s.originGuard(s.requireRole(store.RoleViewer, h))
}

func (s *Server) viewer(h http.HandlerFunc) http.Handler {
	return s.originGuard(s.requireRole(store.RoleViewer, h))
}

func (s *Server) operator(h http.HandlerFunc) http.Handler {
	return s.originGuard(s.requireRole(store.RoleOperator, h))
}

func (s *Server) admin(h http.HandlerFunc) http.Handler {
	return s.originGuard(s.requireRole(store.RoleAdmin, h))
}

// requireRole authenticates via session cookie or API key, then checks role.
func (s *Server) requireRole(minRole string, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if store.Rank(u.Role) < store.Rank(minRole) {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

func (s *Server) authenticate(r *http.Request) (*store.User, error) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return s.Store.UserByAPIKeyHash(store.HashToken(strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))))
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil, store.ErrNotFound
	}
	return s.Store.UserBySession(store.HashToken(c.Value))
}

// originGuard blocks cross-site state changes. SameSite=Lax on the session
// cookie already covers most cases; this closes the rest without a CSRF token.
func (s *Server) originGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			next.ServeHTTP(w, r) // API clients are not browsers
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = r.Header.Get("Referer")
		}
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !s.originAllowed(origin, r.Host) {
			writeError(w, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(raw, requestHost string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Host
	if host == requestHost {
		return true
	}
	for _, allowed := range s.Cfg.TrustedOrigins {
		if strings.EqualFold(strings.TrimSuffix(allowed, "/"), u.Scheme+"://"+host) ||
			strings.EqualFold(allowed, host) {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The header is already sent; nothing useful left to do.
		return
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "already exists")
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// --------------------------------------------------------------- rate limit

// limiter is a tiny fixed-window counter keyed by string.
type limiter struct {
	mu      sync.Mutex
	hits    map[string]*bucket
	lastGC  time.Time
}

type bucket struct {
	count int
	reset time.Time
}

func newLimiter() *limiter {
	return &limiter{hits: map[string]*bucket{}, lastGC: time.Now()}
}

// allow records a hit and reports whether the key is under budget.
func (l *limiter) allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.lastGC) > 10*time.Minute {
		for k, b := range l.hits {
			if now.After(b.reset) {
				delete(l.hits, k)
			}
		}
		l.lastGC = now
	}
	b, ok := l.hits[key]
	if !ok || now.After(b.reset) {
		l.hits[key] = &bucket{count: 1, reset: now.Add(window)}
		return true
	}
	b.count++
	return b.count <= limit
}

