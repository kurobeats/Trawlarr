package api

import (
	"net/http"

	"github.com/kurobeats/trawlarr/internal/tmpl"
)

// templatePreview is the JSON shape the email preview renders.
type templatePreview struct {
	Subject     string   `json:"subject"`
	TextBody    string   `json:"text_body"`
	HTMLBody    string   `json:"html_body"`
	Attachments []string `json:"attachments"`
}

// handlePreviewTemplate renders an email template with sample values. The UI
// puts html_body in a sandboxed iframe, so nothing here can execute.
func (s *Server) handlePreviewTemplate(w http.ResponseWriter, r *http.Request) {
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

	ctx, err := s.previewContext()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not build the preview context")
		return
	}
	render := func(body string) (string, error) { return tmpl.Execute(body, ctx) }

	subject, err := render(t.Subject)
	if err != nil {
		writeError(w, http.StatusBadRequest, "subject: "+err.Error())
		return
	}
	text, err := render(t.TextBody)
	if err != nil {
		writeError(w, http.StatusBadRequest, "text body: "+err.Error())
		return
	}
	html, err := render(t.HTMLBody)
	if err != nil {
		writeError(w, http.StatusBadRequest, "html body: "+err.Error())
		return
	}

	names := make([]string, 0, len(t.AttachmentInfo))
	for _, a := range t.AttachmentInfo {
		names = append(names, a.Name)
	}
	writeJSON(w, http.StatusOK, templatePreview{
		Subject:     subject,
		TextBody:    text,
		HTMLBody:    neutralise(html, ctx),
		Attachments: names,
	})
}
