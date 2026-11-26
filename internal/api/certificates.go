package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kurobeats/trawlarr/internal/store"
	"github.com/kurobeats/trawlarr/internal/tlsutil"
)

// certificateView is a profile plus what the UI needs to show about its cert.
type certificateView struct {
	*store.Certificate
	Status tlsutil.Status `json:"status"`
	Usage  int            `json:"usage"`
}

func (s *Server) handleListCertificates(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	profiles, err := s.Store.Certificates(me.OrgID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]certificateView, 0, len(profiles))
	for _, p := range profiles {
		usage, _ := s.Store.CertificateUsage(me.OrgID, p.ID)
		out = append(out, certificateView{
			Certificate: p,
			Status:      tlsutil.ReadStatus(tlsutil.FromCertificate(p)),
			Usage:       usage,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetCertificate(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := s.Store.Certificate(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	usage, _ := s.Store.CertificateUsage(me.OrgID, id)
	writeJSON(w, http.StatusOK, certificateView{
		Certificate: p,
		Status:      tlsutil.ReadStatus(tlsutil.FromCertificate(p)),
		Usage:       usage,
	})
}

func (s *Server) handleCreateCertificate(w http.ResponseWriter, r *http.Request) {
	s.saveCertificate(w, r, 0)
}

func (s *Server) handleUpdateCertificate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.saveCertificate(w, r, id)
}

func (s *Server) saveCertificate(w http.ResponseWriter, r *http.Request, id int64) {
	me := UserFrom(r)
	var p store.Certificate
	if !decodeJSON(w, r, &p) {
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		writeError(w, http.StatusBadRequest, "certificate name is required")
		return
	}
	domains, err := tlsutil.NormaliseDomains(p.Domains)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(domains) == 0 {
		writeError(w, http.StatusBadRequest, "at least one domain is required")
		return
	}
	if email := strings.TrimSpace(p.Email); email != "" && !strings.Contains(email, "@") {
		writeError(w, http.StatusBadRequest, "email address is not valid")
		return
	}
	if addr := strings.TrimSpace(p.HTTPAddr); addr != "" && !strings.Contains(addr, ":") {
		writeError(w, http.StatusBadRequest, "challenge address must be host:port")
		return
	}
	p.Domains = domains
	p.Email = strings.TrimSpace(p.Email)
	p.CacheDir = strings.TrimSpace(p.CacheDir)
	p.HTTPAddr = strings.TrimSpace(p.HTTPAddr)
	p.OrgID = me.OrgID

	if err := s.Store.SaveCertificate(&p, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "certificate.saved", p.Name, strings.Join(domains, ","), s.clientIP(r))

	usage, _ := s.Store.CertificateUsage(me.OrgID, p.ID)
	out := certificateView{Certificate: &p, Status: tlsutil.ReadStatus(tlsutil.FromCertificate(&p)), Usage: usage}
	if id == 0 {
		writeJSON(w, http.StatusCreated, out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteCertificate(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteCertificate(me.OrgID, id); err != nil {
		if err == store.ErrConflict {
			writeError(w, http.StatusConflict, "a campaign still uses this certificate")
			return
		}
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "certificate.deleted", "", "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleIssueCertificate requests or renews a certificate. Both are the same
// ACME call; only the audit label differs.
func (s *Server) handleIssueCertificate(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	action := r.PathValue("action")
	if action != "request" && action != "renew" {
		writeError(w, http.StatusBadRequest, "action must be request or renew")
		return
	}
	p, err := s.Store.Certificate(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	profile := tlsutil.FromCertificate(p)
	if !profile.AcceptTOS {
		writeError(w, http.StatusBadRequest, "accept the CA terms of service on this profile first")
		return
	}

	// One issuance at a time: two tabs must not race the same domain.
	if !s.tlsMu.TryLock() {
		writeError(w, http.StatusConflict, "a certificate request is already running")
		return
	}
	defer s.tlsMu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	s.Store.Audit(me.OrgID, me.ID, "certificate."+action, p.Name, strings.Join(p.Domains, ","), s.clientIP(r))
	result, err := tlsutil.Request(ctx, profile, func(msg string, args ...any) { s.Log.Info(msg, args...) })
	if err != nil {
		s.Log.Warn("certificate "+action+" failed", "profile", p.Name, "err", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	usage, _ := s.Store.CertificateUsage(me.OrgID, id)
	writeJSON(w, http.StatusOK, map[string]any{
		"certificate": result,
		"status":      tlsutil.ReadStatus(profile),
		"usage":       usage,
	})
}

// certificateHost returns the hostname a campaign's tracking URL points at.
func certificateHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	host := u.Hostname()
	return strings.ToLower(host)
}
