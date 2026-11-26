package api

import (
	"encoding/base64"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"strings"
	"testing"
)

func TestParseEMLSimple(t *testing.T) {
	raw := "From: IT <it@example.com>\r\n" +
		"Subject: =?utf-8?b?UGF5cm9sbCB1cGRhdGU=?=\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Hi Alice,\r\nPlease review.\r\n"

	subject, text, html, err := ParseEML([]byte(raw), false)
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Payroll update" {
		t.Fatalf("subject = %q", subject)
	}
	if !strings.Contains(text, "Hi Alice") {
		t.Fatalf("text = %q", text)
	}
	if html != "" {
		t.Fatalf("unexpected html: %q", html)
	}
}

func TestParseEMLMultipartWithQuotedPrintable(t *testing.T) {
	var body strings.Builder
	body.WriteString("From: it@example.com\r\n")
	body.WriteString("Subject: Multiline\r\n")
	body.WriteString("MIME-Version: 1.0\r\n")
	body.WriteString("Content-Type: multipart/alternative; boundary=BOUND\r\n\r\n")
	body.WriteString("--BOUND\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	qp := quotedprintable.NewWriter(&body)
	qp.Write([]byte("Plain part with a long line that needs wrapping to exercise the decoder path.\r\n"))
	qp.Close()
	body.WriteString("\r\n--BOUND\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	qp = quotedprintable.NewWriter(&body)
	qp.Write([]byte(`<p>Hello</p><a href="https://example.com/login">Sign in</a>`))
	qp.Close()
	body.WriteString("\r\n--BOUND--\r\n")

	_, text, html, err := ParseEML([]byte(body.String()), true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Plain part") {
		t.Fatalf("text = %q", text)
	}
	if !strings.Contains(html, `href="{{.URL}}"`) {
		t.Fatalf("links not converted: %q", html)
	}
}

func TestParseEMLBase64AndCharset(t *testing.T) {
	// "Grüße" in ISO-8859-1, base64 encoded.
	latin1 := []byte{0x47, 0x72, 0xfc, 0xdf, 0x65}
	encoded := base64.StdEncoding.EncodeToString(latin1)
	raw := "Subject: Grüsse\r\nContent-Type: text/plain; charset=iso-8859-1\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + encoded + "\r\n"

	_, text, _, err := ParseEML([]byte(raw), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Grüße") {
		t.Fatalf("charset not converted: %q", text)
	}
}

func TestParseEMLForwardedMessage(t *testing.T) {
	inner := "Subject: original\r\nContent-Type: text/html\r\n\r\n<a href='https://phish.test/click?rid=abcdefghijk'>x</a>"

	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	mw.SetBoundary("FWD")
	buf.WriteString("From: reporter@example.com\r\nSubject: Fwd: invoice\r\n")
	buf.WriteString("Content-Type: " + mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": "FWD"}) + "\r\n\r\n")
	part, _ := mw.CreatePart(mapHeaders("message/rfc822"))
	part.Write([]byte(inner))
	mw.Close()

	subject, _, html, err := ParseEML([]byte(buf.String()), false)
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Fwd: invoice" {
		t.Fatalf("subject = %q", subject)
	}
	_ = html // the forwarded body is a part, not the outer html
}

func TestParseEMLRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "not an email at all"} {
		if _, _, _, err := ParseEML([]byte(raw), false); err == nil {
			t.Errorf("%q was accepted", raw)
		}
	}
	if _, _, _, err := ParseEML([]byte(strings.Repeat("a", maxEmailImport+1)), false); err == nil {
		t.Fatal("oversized message accepted")
	}
}

func TestImportEmailEndpoint(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	raw := "From: it@example.com\r\nSubject: Hello\r\nContent-Type: text/html\r\n\r\n" +
		`<p>Hi</p><a href="https://example.com/x">link</a>`
	got := h.expect(c, http.MethodPost, "/api/v1/templates/import-email", map[string]any{
		"content": raw, "convert_links": true,
	}, http.StatusOK)
	if got["subject"] != "Hello" {
		t.Fatalf("subject = %v", got["subject"])
	}
	if !strings.Contains(got["html_body"].(string), `href="{{.URL}}"`) {
		t.Fatalf("links not converted: %v", got["html_body"])
	}
	if status, _ := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/templates/import-email",
		map[string]string{"content": "nope"}); status != http.StatusBadRequest {
		t.Fatalf("garbage message = %d", status)
	}
}

func mapHeaders(contentType string) map[string][]string {
	return map[string][]string{"Content-Type": {contentType}}
}
