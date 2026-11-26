package api

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"

	"github.com/kurobeats/trawlarr/internal/tmpl"
)

// previewCSP locks the preview down: it is rendered on the admin origin, so it
// must never run scripts, submit forms or navigate away, and it may only be
// framed by us.
const previewCSP = "sandbox; default-src 'none'; img-src * data: blob:; " +
	"media-src *; style-src 'unsafe-inline' *; font-src * data:; " +
	"form-action 'none'; frame-ancestors 'self'"

// previewRecipient is the sample person both previews render against.
func previewRecipient() tmpl.Recipient {
	return tmpl.Recipient{
		FirstName: "Alex", LastName: "Morgan", Email: "alex.morgan@example.com",
		Position: "Finance", Custom: map[string]string{},
	}
}

// previewContext builds the template context used by the previews.
func (s *Server) previewContext() (tmpl.Context, error) {
	return tmpl.NewContext("Security Team <security@example.com>", s.phishBaseURL(), "preview", previewRecipient())
}

// previewPixel is an inline 1x1 GIF, so previews never call the tracking
// endpoint and never show a broken image.
var previewPixel = `<img alt="" src="data:image/gif;base64,` +
	base64.StdEncoding.EncodeToString(pixel) + `" style="display:none">`

// neutralise strips live tracking URLs from rendered preview markup: a click
// must not navigate the preview frame into a 404.
func neutralise(html string, ctx tmpl.Context) string {
	html = strings.ReplaceAll(html, ctx.Tracker, previewPixel)
	html = strings.ReplaceAll(html, ctx.TrackingURL, "#")
	for _, u := range []string{ctx.URL, ctx.OptOutURL, ctx.ReportURL} {
		if u != "" {
			html = strings.ReplaceAll(html, u, "#")
		}
	}
	return html
}

// handlePreviewPage renders a landing page with sample values for the preview
// iframe. Nothing is stored and no tracking request is made.
func (s *Server) handlePreviewPage(w http.ResponseWriter, r *http.Request) {
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	page, err := s.Store.Page(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	ctx, err := s.previewContext()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not build the preview context")
		return
	}
	rendered, err := tmpl.Execute(page.HTML, ctx)
	if err != nil {
		// Show the template error rather than a blank frame.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", previewCSP)
		w.Header().Del("X-Frame-Options")
		io.WriteString(w, `<!doctype html><meta charset="utf-8"><body style="font:14px system-ui;padding:2rem">`+
			`<h1>Template error</h1><pre>`+escapeHTML(err.Error())+`</pre></body>`)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", previewCSP)
	// The preview is meant to be framed by the admin UI.
	w.Header().Del("X-Frame-Options")
	io.WriteString(w, neutralise(rendered, ctx))
}

// escapeHTML keeps a template error from injecting markup into the frame.
func escapeHTML(s string) string {
	r := []rune{}
	for _, c := range s {
		switch c {
		case '<':
			r = append(r, '&', 'l', 't', ';')
		case '>':
			r = append(r, '&', 'g', 't', ';')
		case '&':
			r = append(r, '&', 'a', 'm', 'p', ';')
		default:
			r = append(r, c)
		}
	}
	return string(r)
}
