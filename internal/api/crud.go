package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/kurobeats/trawlarr/internal/mail"
	"github.com/kurobeats/trawlarr/internal/store"
)

// ------------------------------------------------------------------ groups

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	groups, err := s.Store.Groups(me.OrgID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	g, err := s.Store.Group(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

type groupPayload struct {
	Name    string         `json:"name"`
	Targets []store.Target `json:"targets"`
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	s.saveGroup(w, r, 0)
}

func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.saveGroup(w, r, id)
}

func (s *Server) saveGroup(w http.ResponseWriter, r *http.Request, id int64) {
	me := UserFrom(r)
	var req groupPayload
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "group name is required")
		return
	}
	for _, t := range req.Targets {
		if !strings.Contains(t.Email, "@") {
			writeError(w, http.StatusBadRequest, "invalid recipient email: "+t.Email)
			return
		}
	}
	g := &store.Group{OrgID: me.OrgID, Name: req.Name, Targets: req.Targets}
	if err := s.Store.SaveGroup(g, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "group.saved", g.Name, strconv.Itoa(len(req.Targets))+" recipients", s.clientIP(r))
	out, err := s.Store.Group(me.OrgID, g.ID)
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

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteGroup(me.OrgID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "group.deleted", strconv.FormatInt(id, 10), "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleImportGroupCSV parses a recipient list. A header row is optional;
// without one the columns are first_name,last_name,email,position.
func (s *Server) handleImportGroupCSV(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart 'file' field")
		return
	}
	defer file.Close()
	targets, err := ParseTargetCSV(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": targets, "count": len(targets)})
}

// ParseTargetCSV reads recipients from CSV, accepting common export layouts.
func ParseTargetCSV(rd io.Reader) ([]store.Target, error) {
	return store.ParseTargetsCSV(rd)
}

// --------------------------------------------------------------- templates

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	items, err := s.Store.Templates(me.OrgID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	t, err := s.Store.Template(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	s.saveTemplate(w, r, 0)
}

func (s *Server) handleUpdateTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.saveTemplate(w, r, id)
}

func (s *Server) saveTemplate(w http.ResponseWriter, r *http.Request, id int64) {
	me := UserFrom(r)
	var t store.Template
	if !decodeJSON(w, r, &t) {
		return
	}
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		writeError(w, http.StatusBadRequest, "template name is required")
		return
	}
	t.OrgID = me.OrgID
	if err := s.Store.SaveTemplate(&t, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "template.saved", t.Name, "", s.clientIP(r))
	out, err := s.Store.Template(me.OrgID, t.ID)
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

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteTemplate(me.OrgID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "template.deleted", strconv.FormatInt(id, 10), "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ------------------------------------------------------------------- pages

func (s *Server) handleListPages(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	items, err := s.Store.Pages(me.OrgID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleGetPage(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := s.Store.Page(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleCreatePage(w http.ResponseWriter, r *http.Request) { s.savePage(w, r, 0) }

func (s *Server) handleUpdatePage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.savePage(w, r, id)
}

func (s *Server) savePage(w http.ResponseWriter, r *http.Request, id int64) {
	me := UserFrom(r)
	var p store.Page
	if !decodeJSON(w, r, &p) {
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		writeError(w, http.StatusBadRequest, "page name is required")
		return
	}
	p.OrgID = me.OrgID
	if err := s.Store.SavePage(&p, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "page.saved", p.Name, "", s.clientIP(r))
	out, err := s.Store.Page(me.OrgID, p.ID)
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

func (s *Server) handleDeletePage(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeletePage(me.OrgID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "page.deleted", strconv.FormatInt(id, 10), "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleImportLandingPage clones a live page and rewrites its asset URLs so it
// renders from Trawlarr, so imported pages do not arrive with broken images.
func (s *Server) handleImportLandingPage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	html, finalURL, err := FetchLandingPage(r.Context(), req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": html, "source_url": finalURL})
}

// FetchLandingPage downloads and absolutises a page's links.
func FetchLandingPage(ctx context.Context, raw string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", errors.New("a valid http(s) URL is required")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Trawlarr/1.0)")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("upstream returned %s", resp.Status)
	}
	doc, err := goquery.NewDocumentFromReader(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", "", err
	}
	base := resp.Request.URL
	absolutise := func(sel, attr string) {
		doc.Find(sel).Each(func(_ int, el *goquery.Selection) {
			v, ok := el.Attr(attr)
			if !ok || v == "" || strings.HasPrefix(v, "data:") || strings.HasPrefix(v, "#") {
				return
			}
			ref, err := url.Parse(v)
			if err != nil {
				return
			}
			el.SetAttr(attr, base.ResolveReference(ref).String())
		})
	}
	absolutise("img", "src")
	absolutise("link", "href")
	absolutise("script", "src")
	absolutise("form", "action")
	absolutise("[data-src]", "data-src")
	doc.Find("[integrity]").RemoveAttr("integrity")
	doc.Find("[crossorigin]").RemoveAttr("crossorigin")
	doc.Find(`meta[http-equiv]`).Each(func(_ int, el *goquery.Selection) {
		if v, _ := el.Attr("http-equiv"); strings.Contains(strings.ToLower(v), "content-security-policy") {
			el.Remove()
		}
	})
	doc.Find("base").Remove()
	var buf bytes.Buffer
	if err := goquery.Render(&buf, doc.Selection); err != nil {
		return "", "", err
	}
	return buf.String(), base.String(), nil
}

// --------------------------------------------------------- sending profiles

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	items, err := s.Store.Profiles(me.OrgID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := s.Store.Profile(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	p.Password = ""
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) { s.saveProfile(w, r, 0) }

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.saveProfile(w, r, id)
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request, id int64) {
	me := UserFrom(r)
	var p store.Profile
	if !decodeJSON(w, r, &p) {
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		writeError(w, http.StatusBadRequest, "profile name is required")
		return
	}
	if !strings.Contains(p.FromAddress, "@") {
		writeError(w, http.StatusBadRequest, "from address must be an email address")
		return
	}
	p.OrgID = me.OrgID
	p.Host, p.Port = splitHostPort(p.Host, p.Port)
	if err := s.Store.SaveProfile(&p, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "profile.saved", p.Name, p.Host, s.clientIP(r))
	out, err := s.Store.Profile(me.OrgID, p.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out.Password = ""
	if id == 0 {
		writeJSON(w, http.StatusCreated, out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func splitHostPort(host string, port int) (string, int) {
	host = strings.TrimSpace(host)
	if h, p, err := netSplitHostPort(host); err == nil {
		if n, err := strconv.Atoi(p); err == nil {
			return h, n
		}
	}
	if port == 0 {
		port = 587
	}
	return strings.TrimSuffix(host, ":"), port
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteProfile(me.OrgID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "profile.deleted", strconv.FormatInt(id, 10), "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTestProfile(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		To string `json:"to"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !strings.Contains(req.To, "@") {
		writeError(w, http.StatusBadRequest, "recipient email is required")
		return
	}
	p, err := s.Store.Profile(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	ctx, cancel := timeoutCtx(r, 45*time.Second)
	defer cancel()
	if err := s.Sender.Send(ctx, p, testMail(req.To, p.FromAddress)); err != nil {
		_ = s.Store.SetProfileTest(me.OrgID, id, err.Error())
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = s.Store.SetProfileTest(me.OrgID, id, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ------------------------------------------------------------- attachments

func (s *Server) handleListAttachments(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	items, err := s.Store.Attachments(me.OrgID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart 'file' field")
		return
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := filepath.Base(header.Filename)
	ctype := header.Header.Get("Content-Type")
	if ctype == "" {
		ctype = mime.TypeByExtension(path.Ext(name))
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	id, err := s.Store.CreateAttachment(me.OrgID, name, ctype, content)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "attachment.uploaded", name, formatBytes(int64(len(content))), s.clientIP(r))
	items, _ := s.Store.Attachments(me.OrgID)
	for _, a := range items {
		if a.ID == id {
			writeJSON(w, http.StatusCreated, a)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) handleDownloadAttachment(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	name, ctype, content, err := s.Store.AttachmentContent(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
	w.Write(content)
}

func (s *Server) handleDeleteAttachment(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteAttachment(me.OrgID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- webhooks

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	items, err := s.Store.Webhooks(me.OrgID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	for _, w := range items {
		w.Secret = ""
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) { s.saveWebhook(w, r, 0) }

func (s *Server) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.saveWebhook(w, r, id)
}

func (s *Server) saveWebhook(w http.ResponseWriter, r *http.Request, id int64) {
	me := UserFrom(r)
	var wh store.Webhook
	if !decodeJSON(w, r, &wh) {
		return
	}
	wh.Name = strings.TrimSpace(wh.Name)
	u, err := url.Parse(wh.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeError(w, http.StatusBadRequest, "webhook URL must be http(s)")
		return
	}
	if wh.Name == "" {
		wh.Name = u.Host
	}
	if len(wh.Events) == 0 {
		wh.Events = []string{"*"}
	}
	wh.OrgID = me.OrgID
	if err := s.Store.SaveWebhook(&wh, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "webhook.saved", wh.Name, wh.URL, s.clientIP(r))
	out, err := s.Store.Webhook(me.OrgID, wh.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out.Secret = ""
	if id == 0 {
		writeJSON(w, http.StatusCreated, out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteWebhook(me.OrgID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ------------------------------------------------------------------- misc

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := s.Store.AuditLog(me.OrgID, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": Version})
}

// serveUI serves the embedded single-page app.
func (s *Server) serveUI() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "." {
			name = "index.html"
		}
		f, err := s.UI.Open(name)
		if err != nil {
			// Unknown path: hand it to the SPA router.
			name = "index.html"
			if f, err = s.UI.Open(name); err != nil {
				http.Error(w, "UI assets are missing; run `make ui`", http.StatusInternalServerError)
				return
			}
		}
		defer f.Close()
		if strings.HasSuffix(name, ".html") {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		if ctype := mime.TypeByExtension(path.Ext(name)); ctype != "" {
			w.Header().Set("Content-Type", ctype)
		}
		http.ServeContent(w, r, name, time.Time{}, f.(io.ReadSeeker))
	})
}

// Version is stamped at build time.
var Version = "dev"

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// timeoutCtx bounds an upstream operation such as an SMTP test.
func timeoutCtx(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

func netSplitHostPort(host string) (string, string, error) { return net.SplitHostPort(host) }

func testMail(to, from string) mail.Message { return mail.TestMessage(to, from) }
