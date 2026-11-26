package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/kurobeats/trawlarr/internal/notify"
	"github.com/kurobeats/trawlarr/internal/store"
	"github.com/kurobeats/trawlarr/internal/tmpl"
)

// pixel is a 1x1 transparent GIF.
var pixel = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00,
	0x00, 0xff, 0xff, 0xff, 0x00, 0x00, 0x00, 0x21, 0xf9, 0x04, 0x01, 0x00,
	0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00,
	0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3b,
}

// serveNotFound returns an ordinary-looking 404. A distinctive error page or
// an identifying header tells a scanner exactly what is running here.
func serveNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(notFoundPage))
}

const notFoundPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>404 Not Found</title></head>
<body><h1>Not Found</h1><p>The requested URL was not found on this server.</p></body></html>`

// trackRequest resolves a rid, applies a per-IP budget and returns the target.
func (s *Server) trackRequest(w http.ResponseWriter, r *http.Request) (*store.CampaignTarget, *store.Campaign, bool) {
	if !s.limiter.allow("track:"+s.clientIP(r), 120, time.Minute) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return nil, nil, false
	}
	rid := r.URL.Query().Get("rid")
	if rid == "" {
		serveNotFound(w)
		return nil, nil, false
	}
	t, c, err := s.Store.TargetByRID(rid)
	if err != nil {
		serveNotFound(w)
		return nil, nil, false
	}
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	return t, c, true
}

func (s *Server) record(t *store.CampaignTarget, c *store.Campaign, evType, message string, r *http.Request, data *store.Capture, capturePasswords bool) {
	ip := s.clientIP(r)
	ua := r.UserAgent()
	if err := s.Store.Track(t, evType, message, ip, ua, data, capturePasswords); err != nil {
		s.Log.Warn("track write failed", "rid", t.RID, "event", evType, "err", err)
		return
	}
	s.Hub.Emit(c.OrgID, notify.Payload{
		Event: evType, CampaignID: c.ID, Campaign: c.Name,
		Recipient: t.Email, RID: t.RID, Message: message, IP: ip,
		Data: data, At: time.Now().UTC(),
	})
}

// handleOpen serves the tracking pixel. Repeat opens are not re-recorded so an
// email scanner hitting the link first does not inflate the open count.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	t, c, ok := s.trackRequest(w, r)
	if !ok {
		return
	}
	if t.OpenedAt.IsZero() {
		s.record(t, c, store.EvEmailOpened, "Email opened", r, nil, false)
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Content-Length", "43")
	w.Write(pixel)
}

// handleClick records the click and serves the landing page in one request, so
// prefetching a link does not create a phantom redirect chain.
func (s *Server) handleClick(w http.ResponseWriter, r *http.Request) {
	t, c, ok := s.trackRequest(w, r)
	if !ok {
		return
	}
	if t.ClickedAt.IsZero() {
		s.record(t, c, store.EvLinkClicked, "Clicked link", r, nil, false)
	}
	if c.ProxyURL != "" {
		s.relay(w, r, t, c, "")
		return
	}
	s.serveLanding(w, r, t, c)
}

func (s *Server) serveLanding(w http.ResponseWriter, r *http.Request, t *store.CampaignTarget, c *store.Campaign) {
	page, err := s.Store.Page(c.OrgID, c.PageID)
	if err != nil {
		serveNotFound(w)
		return
	}
	ctxData, err := tmpl.NewContext("", s.campaignBaseURL(c), t.RID, tmpl.Recipient{
		FirstName: t.FirstName, LastName: t.LastName, Email: t.Email,
		Position: t.Position, Custom: t.Custom,
	})
	if err != nil {
		http.Error(w, "could not render landing page", http.StatusInternalServerError)
		return
	}
	rendered, err := tmpl.Execute(page.HTML, ctxData)
	if err != nil {
		http.Error(w, "landing page template error", http.StatusInternalServerError)
		return
	}
	submit := "/submit?rid=" + url.QueryEscape(t.RID)
	if rewritten, err := rewriteForms(rendered, submit); err == nil {
		rendered = rewritten
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(rendered))
}

// rewriteForms points every form at the capture endpoint.
func rewriteForms(html, action string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	doc.Find("form").Each(func(_ int, sel *goquery.Selection) {
		sel.SetAttr("action", action)
		sel.SetAttr("method", "post")
	})
	var buf bytes.Buffer
	if err := goquery.Render(&buf, doc.Selection); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// handleSubmit records the landing page submission, then redirects.
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	t, c, ok := s.trackRequest(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	page, pageErr := s.Store.Page(c.OrgID, c.PageID)
	capture := pageErr == nil && page.Capture

	if !capture {
		s.record(t, c, store.EvDataSubmitted, "Form submitted", r, nil, false)
	} else {
		captured := &store.Capture{Fields: make([]string, 0, len(r.PostForm))}
		for k := range r.PostForm {
			if k != "rid" {
				captured.Fields = append(captured.Fields, k)
			}
		}
		sort.Strings(captured.Fields)
		// Values are only stored when the page explicitly opts in.
		if page.CaptureValues {
			captured.Values = map[string]string{}
			for _, k := range captured.Fields {
				if v := r.PostForm[k]; len(v) > 0 {
					captured.Values[k] = v[0]
				}
			}
		}
		s.record(t, c, store.EvDataSubmitted, "Submitted data", r, captured, page.CapturePasswords)
	}

	if pageErr == nil && page.RedirectURL != "" {
		target, err := tmpl.Execute(page.RedirectURL, tmpl.Recipient{
			FirstName: t.FirstName, LastName: t.LastName, Email: t.Email,
			Position: t.Position, Custom: t.Custom,
		})
		if err == nil && isHTTPURL(target) {
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(trainingPage))
}

// handleReport is hit when a recipient uses the "report phish" link.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	t, c, ok := s.trackRequest(w, r)
	if !ok {
		return
	}
	if t.ReportedAt.IsZero() {
		s.record(t, c, store.EvEmailReported, "Reported as phishing", r, nil, false)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Report received</title>
<body style="font-family:system-ui;max-width:40rem;margin:4rem auto;padding:0 1.5rem">
<h1>Thanks — report received</h1>
<p>Our security team has been notified. Reports like yours are the fastest way to catch real phishing.</p></body>`))
}

// handleOptOut honours a do-not-contact request for future simulations.
func (s *Server) handleOptOut(w http.ResponseWriter, r *http.Request) {
	t, c, ok := s.trackRequest(w, r)
	if !ok {
		return
	}
	if err := s.Store.OptOut(t.Email); err != nil {
		s.Log.Warn("opt-out failed", "err", err)
	}
	s.record(t, c, store.EvTargetOptedOut, "Opted out of simulations", r, nil, false)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Removed</title>
<body style="font-family:system-ui;max-width:40rem;margin:4rem auto;padding:0 1.5rem">
<h1>You're off the list</h1>
<p>You will not receive further simulated phishing emails from this programme.</p></body>`))
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// campaignBaseURL resolves the origin used in tracking links: the campaign's
// own URL when set, otherwise the configured phishing listener.
func (s *Server) campaignBaseURL(c *store.Campaign) string {
	if c.URL != "" {
		return c.URL
	}
	return s.Cfg.PhishServer.BaseURL()
}

const trainingPage = `<!doctype html><meta charset="utf-8"><title>Simulated phishing test</title>
<body style="font-family:system-ui;max-width:42rem;margin:4rem auto;padding:0 1.5rem;line-height:1.6">
<h1>This was a phishing simulation</h1>
<p>You entered credentials on a page created by your security team. No real account was affected and nothing you typed was used to sign in.</p>
<p>Next time, check the sender address, hover over links before clicking, and report anything suspicious.</p></body>`
