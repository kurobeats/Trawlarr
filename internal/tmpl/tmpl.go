// Package tmpl renders campaign templates with per-recipient context.
package tmpl

import (
	"bytes"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"text/template"
)

// Recipient is the per-target data available to a template.
type Recipient struct {
	FirstName string
	LastName  string
	Email     string
	Position  string
	Custom    map[string]string
}

// Context is what a template sees. Field names follow the de-facto template
// vocabulary so existing templates render unchanged.
type Context struct {
	Recipient
	From        string
	URL         string
	Tracker     string
	TrackingURL string
	OptOutURL   string
	ReportURL   string
	RId         string
	BaseURL     string
	QRCode      string
}

// NewContext builds the template context for one recipient.
// urlTemplate may itself contain placeholders (existing templates do this).
func NewContext(fromAddress, baseURL, rid string, r Recipient) (Context, error) {
	name := fromAddress
	if addr, err := mail.ParseAddress(fromAddress); err == nil {
		name = addr.Name
		if name == "" {
			name = addr.Address
		}
	}
	// Allow per-recipient URL customisation such as https://{{.Email}}.example.com
	rendered, err := Execute(baseURL, r)
	if err != nil {
		return Context{}, err
	}
	base, err := url.Parse(rendered)
	if err != nil {
		return Context{}, err
	}
	origin := *base
	origin.Path = ""
	origin.RawQuery = ""

	link := func(route string) string {
		u := *base
		u.Path = strings.TrimSuffix(u.Path, "/") + route
		q := u.Query()
		q.Set("rid", rid)
		u.RawQuery = q.Encode()
		return u.String()
	}
	landing := link("/click")
	pixel := link("/open.gif")
	return Context{
		Recipient:   r,
		From:        name,
		URL:         landing,
		TrackingURL: pixel,
		Tracker:     fmt.Sprintf("<img alt='' style='display:none' src='%s'/>", pixel),
		OptOutURL:   link("/optout"),
		ReportURL:   link("/report"),
		RId:         rid,
		BaseURL:     origin.String(),
	}, nil
}

// Execute renders one template body.
func Execute(text string, data any) (string, error) {
	if text == "" {
		return "", nil
	}
	t, err := template.New("t").Option("missingkey=zero").Parse(text)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
