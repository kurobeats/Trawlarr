package api

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html/charset"
)

const maxEmailImport = 8 << 20

// handleImportEmail turns a raw .eml into template content so an existing
// message can be reused without retyping it.
func (s *Server) handleImportEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content      string `json:"content"`
		ConvertLinks bool   `json:"convert_links"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	subject, textBody, htmlBody, err := ParseEML([]byte(req.Content), req.ConvertLinks)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Store.Audit(UserFrom(r).OrgID, UserFrom(r).ID, "template.imported", "email", "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]string{
		"subject": subject, "text_body": textBody, "html_body": htmlBody,
	})
}

type emlParts struct {
	text string
	html string
}

// ParseEML extracts the subject, plain text and HTML from a raw message,
// optionally pointing every link at {{.URL}}.
func ParseEML(raw []byte, convertLinks bool) (string, string, string, error) {
	if len(raw) == 0 {
		return "", "", "", errors.New("empty message")
	}
	if len(raw) > maxEmailImport {
		return "", "", "", errors.New("message is larger than 8MB")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", "", "", fmt.Errorf("not a valid email message: %w", err)
	}
	subject := decodeWords(msg.Header.Get("Subject"))
	parts := &emlParts{}
	if err := walkMIME(headerReader(textproto.MIMEHeader(msg.Header), msg.Body), 0, parts); err != nil {
		return "", "", "", err
	}
	htmlBody := parts.html
	if convertLinks && htmlBody != "" {
		htmlBody = pointLinksAtTemplate(htmlBody)
	}
	if subject == "" && parts.text == "" && htmlBody == "" {
		return "", "", "", errors.New("no subject or body found")
	}
	return subject, parts.text, htmlBody, nil
}

// walkMIME reads one message entity and recurses into its parts.
func walkMIME(rd io.Reader, depth int, parts *emlParts) error {
	if depth > 4 {
		return nil
	}
	msg, err := mail.ReadMessage(rd)
	if err != nil {
		// A part without headers is body-only; treat it as text.
		body, _ := io.ReadAll(io.LimitReader(rd, maxEmailImport))
		if len(parts.text) == 0 {
			parts.text = string(body)
		}
		return nil
	}
	mediaType, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	body := decodeTransferEncoding(msg.Header.Get("Content-Transfer-Encoding"), msg.Body)

	switch {
	case strings.HasPrefix(mediaType, "multipart/") && params["boundary"] != "":
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("malformed multipart body: %w", err)
			}
			// Re-wrap the part so walkMIME sees its headers.
			if err := walkMIME(headerReader(part.Header, part), depth+1, parts); err != nil {
				return err
			}
		}
	case mediaType == "message/rfc822":
		if err := walkMIME(body, depth+1, parts); err != nil {
			return err
		}
	case strings.HasPrefix(mediaType, "text/html"):
		if parts.html == "" {
			parts.html = readText(body, charsetOf(params))
		}
	case strings.HasPrefix(mediaType, "text/plain"):
		if parts.text == "" {
			parts.text = readText(body, charsetOf(params))
		}
	}
	return nil
}

// headerReader rebuilds an entity that has headers followed by a body.
func headerReader(h textproto.MIMEHeader, body io.Reader) io.Reader {
	var buf bytes.Buffer
	for k, values := range h {
		for _, v := range values {
			fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
		}
	}
	buf.WriteString("\r\n")
	return io.MultiReader(&buf, body)
}

func decodeTransferEncoding(encoding string, rd io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, stripWhitespace(rd))
	case "quoted-printable":
		return quotedprintable.NewReader(rd)
	default:
		return rd
	}
}

// stripWhitespace removes line breaks that base64 decoders reject.
func stripWhitespace(rd io.Reader) io.Reader {
	raw, err := io.ReadAll(rd)
	if err != nil {
		return bytes.NewReader(nil)
	}
	cleaned := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, string(raw))
	return strings.NewReader(cleaned)
}

func charsetOf(params map[string]string) string {
	if cs := params["charset"]; cs != "" {
		return cs
	}
	return "utf-8"
}

func readText(rd io.Reader, charsetLabel string) string {
	raw, err := io.ReadAll(io.LimitReader(rd, maxEmailImport))
	if err != nil {
		return ""
	}
	utf8Reader, err := charset.NewReaderLabel(charsetLabel, bytes.NewReader(raw))
	if err != nil {
		return string(raw)
	}
	converted, err := io.ReadAll(io.LimitReader(utf8Reader, maxEmailImport))
	if err != nil {
		return string(raw)
	}
	return string(converted)
}

func decodeWords(s string) string {
	dec := new(mime.WordDecoder)
	if out, err := dec.DecodeHeader(s); err == nil {
		return out
	}
	return s
}

// pointLinksAtTemplate rewrites every anchor so a click lands on the tracked URL.
func pointLinksAtTemplate(htmlBody string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err != nil {
		return htmlBody
	}
	doc.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
		a.SetAttr("href", "{{.URL}}")
	})
	out, err := doc.Html()
	if err != nil {
		return htmlBody
	}
	return out
}
