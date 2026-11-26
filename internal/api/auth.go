package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kurobeats/trawlarr/internal/auth"
	"github.com/kurobeats/trawlarr/internal/store"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTP     string `json:"totp"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.limiter.allow("login:"+ip, 10, 15*time.Minute) {
		writeError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !s.limiter.allow("login-user:"+strings.ToLower(req.Username), 10, 15*time.Minute) {
		writeError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}
	u, err := s.Store.UserByUsername(req.Username)
	if err != nil {
		// Burn comparable time so missing users are not distinguishable.
		auth.VerifyPassword("$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", req.Password)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if u.Disabled {
		writeError(w, http.StatusUnauthorized, "account disabled")
		return
	}
	if !auth.VerifyPassword(u.PasswordHash, req.Password) {
		s.Store.Audit(u.OrgID, u.ID, "login.failed", u.Username, "", ip)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if u.TOTPEnabled {
		if req.TOTP == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "totp required", "totp_required": true})
			return
		}
		if !auth.VerifyTOTP(u.TOTPSecret, req.TOTP, time.Now()) {
			s.Store.Audit(u.OrgID, u.ID, "login.failed_totp", u.Username, "", ip)
			writeError(w, http.StatusUnauthorized, "invalid authenticator code")
			return
		}
	}
	// Transparently upgrade legacy bcrypt hashes to argon2id.
	if auth.NeedsRehash(u.PasswordHash) {
		if hash, err := auth.HashPassword(req.Password); err == nil {
			_ = s.Store.SetPassword(u.ID, hash)
		}
	}
	s.startSession(w, r, u)
	s.Store.Audit(u.OrgID, u.ID, "login.success", u.Username, "", ip)
	writeJSON(w, http.StatusOK, s.meResponse(u))
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) {
	token := auth.NewToken(32)
	ttl := time.Duration(s.Cfg.SessionTTLHours) * time.Hour
	if err := s.Store.CreateSession(store.HashToken(token), u.ID, ttl, s.clientIP(r), r.UserAgent()); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	// #nosec G124 -- HttpOnly and SameSite are always set; Secure follows the deployment config.
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.Cfg.SecureCookies,
		Expires:  time.Now().Add(ttl),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = s.Store.DeleteSession(store.HashToken(c.Value))
	}
	// #nosec G124 -- clearing the cookie; no value and no transport need.
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.meResponse(UserFrom(r)))
}

func (s *Server) meResponse(u *store.User) map[string]any {
	out := map[string]any{
		"id":           u.ID,
		"username":     u.Username,
		"email":        u.Email,
		"role":         u.Role,
		"totp_enabled": u.TOTPEnabled,
		"has_api_key":  u.HasAPIKey,
		"org_id":       u.OrgID,
	}
	if org, err := s.Store.Org(u.OrgID); err == nil {
		out["org_name"] = org.Name
	}
	out["phish_url"] = s.phishBaseURL()
	out["role_rank"] = store.Rank(u.Role)
	return out
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u := UserFrom(r)
	if !auth.VerifyPassword(u.PasswordHash, req.Current) {
		writeError(w, http.StatusForbidden, "current password is incorrect")
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.SetPassword(u.ID, hash); err != nil {
		writeStoreError(w, err)
		return
	}
	// Rotate the session so other devices are logged out.
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = s.Store.DeleteSession(store.HashToken(c.Value))
	}
	s.startSession(w, r, u)
	s.Store.Audit(u.OrgID, u.ID, "user.password_changed", u.Username, "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRegenerateAPIKey(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r)
	key := auth.NewToken(32)
	if err := s.Store.SetAPIKey(u.ID, store.HashToken(key)); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(u.OrgID, u.ID, "user.apikey_rotated", u.Username, "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]string{"api_key": key})
}

func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r)
	if u.TOTPEnabled {
		writeError(w, http.StatusConflict, "two-factor authentication is already enabled")
		return
	}
	secret := auth.TOTPSecret()
	if err := s.Store.SetTOTP(u.ID, secret, false); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": secret,
		"url":    auth.TOTPURL(secret, u.Username, "Trawlarr"),
	})
}

func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u := UserFrom(r)
	if u.TOTPSecret == "" {
		writeError(w, http.StatusBadRequest, "run setup first")
		return
	}
	if !auth.VerifyTOTP(u.TOTPSecret, req.Code, time.Now()) {
		writeError(w, http.StatusBadRequest, "invalid authenticator code")
		return
	}
	if err := s.Store.SetTOTP(u.ID, u.TOTPSecret, true); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(u.OrgID, u.ID, "user.totp_enabled", u.Username, "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u := UserFrom(r)
	if !auth.VerifyPassword(u.PasswordHash, req.Password) {
		writeError(w, http.StatusForbidden, "password is incorrect")
		return
	}
	if err := s.Store.SetTOTP(u.ID, "", false); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(u.OrgID, u.ID, "user.totp_disabled", u.Username, "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ------------------------------------------------------------------- users

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r)
	users, err := s.Store.UsersByOrg(u.OrgID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if roleValid(req.Role) == false {
		writeError(w, http.StatusBadRequest, "role must be admin, operator or viewer")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	me := UserFrom(r)
	u := &store.User{OrgID: me.OrgID, Username: req.Username, Email: req.Email, Role: req.Role, PasswordHash: hash}
	if err := s.Store.CreateUser(u); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "user.created", u.Username, "role="+u.Role, s.clientIP(r))
	out, _ := s.Store.UserByID(u.ID)
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	target, err := s.Store.UserByID(id)
	if err != nil || target.OrgID != me.OrgID {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req struct {
		Email    *string `json:"email"`
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
		Password *string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Role != nil {
		if !roleValid(*req.Role) {
			writeError(w, http.StatusBadRequest, "role must be admin, operator or viewer")
			return
		}
		if target.Role == store.RoleAdmin && *req.Role != store.RoleAdmin {
			if n, _ := s.Store.CountAdmins(me.OrgID); n <= 1 {
				writeError(w, http.StatusBadRequest, "cannot demote the last admin")
				return
			}
		}
		target.Role = *req.Role
	}
	if req.Email != nil {
		target.Email = *req.Email
	}
	if req.Disabled != nil {
		if *req.Disabled && target.ID == me.ID {
			writeError(w, http.StatusBadRequest, "cannot disable your own account")
			return
		}
		if *req.Disabled && target.Role == store.RoleAdmin {
			if n, _ := s.Store.CountAdmins(me.OrgID); n <= 1 {
				writeError(w, http.StatusBadRequest, "cannot disable the last admin")
				return
			}
		}
		target.Disabled = *req.Disabled
	}
	if err := s.Store.UpdateUser(target); err != nil {
		writeStoreError(w, err)
		return
	}
	if req.Password != nil && *req.Password != "" {
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = s.Store.SetPassword(target.ID, hash)
	}
	s.Store.Audit(me.OrgID, me.ID, "user.updated", target.Username, "", s.clientIP(r))
	out, _ := s.Store.UserByID(target.ID)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if id == me.ID {
		writeError(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}
	target, err := s.Store.UserByID(id)
	if err != nil || target.OrgID != me.OrgID {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if target.Role == store.RoleAdmin {
		if n, _ := s.Store.CountAdmins(me.OrgID); n <= 1 {
			writeError(w, http.StatusBadRequest, "cannot delete the last admin")
			return
		}
	}
	if err := s.Store.DeleteUser(id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "user.deleted", target.Username, "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func roleValid(role string) bool {
	return role == store.RoleAdmin || role == store.RoleOperator || role == store.RoleViewer
}

// pathID parses the {id} path segment.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}
