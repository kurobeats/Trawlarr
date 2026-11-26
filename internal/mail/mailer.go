// Package mail sends campaign email over SMTP.
package mail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/mail"
	"path"
	"strconv"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"

	"github.com/kurobeats/trawlarr/internal/store"
)

// Attachment is an outbound file.
type Attachment struct {
	Name        string
	ContentType string
	Content     []byte
}

// Message is one personalised email.
type Message struct {
	To           string
	ToName       string
	From         string
	EnvelopeFrom string
	Subject      string
	Text         string
	HTML         string
	Headers      []store.Header
	Attachments  []Attachment
	// XMailer overrides the User-Agent header. Empty means no header.
	XMailer string
	// RID is embedded in the Message-ID so a reported message can be matched
	// back to its recipient without adding an identifying header.
	RID string
}

// Sender delivers messages. Tests swap in a fake.
type Sender interface {
	Send(ctx context.Context, p *store.Profile, m Message) error
}

// SMTP is the production Sender.
type SMTP struct {
	Log     *slog.Logger
	Timeout time.Duration
}

// TestMessage is the body used by the "send test email" button.
func TestMessage(to, from string) Message {
	body := "Sending profile test.\n\nIf you can read this, delivery is working.\n"
	return Message{
		To:      to,
		From:    from,
		Subject: "Delivery test",
		Text:    body,
		HTML:    "<p>" + strings.ReplaceAll(strings.TrimSpace(body), "\n", "<br>") + "</p>",
	}
}

// Send delivers one message through the profile's SMTP server.
func (s *SMTP) Send(ctx context.Context, p *store.Profile, m Message) error {
	if p.Host == "" {
		return fmt.Errorf("sending profile %q has no host", p.Name)
	}
	if _, err := mail.ParseAddress(m.From); err != nil {
		return fmt.Errorf("invalid from address %q: %w", m.From, err)
	}
	if _, err := mail.ParseAddress(m.To); err != nil {
		return fmt.Errorf("invalid recipient %q: %w", m.To, err)
	}
	port := p.Port
	if port == 0 {
		port = 587
	}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	domain := fromDomain(m.From)

	opts := []gomail.Option{
		gomail.WithPort(port),
		gomail.WithTimeout(timeout),
		gomail.WithDomain(domain),
		gomail.WithHELO(domain),
		gomail.WithTLSConfig(tlsConfigIgnoringVerification(p.IgnoreCert)),
	}
	switch {
	case port == 465:
		opts = append(opts, gomail.WithSSL(), gomail.WithTLSPortPolicy(gomail.TLSMandatory))
	case p.IgnoreCert:
		// WithTLSConfig already skips verification; still require STARTTLS.
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	default:
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSOpportunistic))
	}
	if p.Username != "" {
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthPlain),
			gomail.WithUsername(p.Username), gomail.WithPassword(p.Password))
	}

	msg := gomail.NewMsg(gomail.WithNoDefaultUserAgent())
	if err := msg.FromFormat(displayName(m.From), addressOnly(m.From)); err != nil {
		return err
	}
	if m.ToName != "" {
		if err := msg.AddToFormat(m.ToName, m.To); err != nil {
			return fmt.Errorf("recipient %q: %w", m.To, err)
		}
	} else if err := msg.To(m.To); err != nil {
		return fmt.Errorf("recipient %q: %w", m.To, err)
	}
	msg.Subject(m.Subject)
	msg.SetDate()
	// Message-ID must come from the sending domain, never the local hostname.
	msg.SetMessageIDWithValue(messageIDLocal(m.RID) + "@" + domain)
	// Only advertise an X-Mailer when the profile asks for one: a tool-specific
	// or missing header is itself a fingerprint.
	if m.XMailer != "" {
		msg.SetGenHeader(gomail.Header("X-Mailer"), m.XMailer)
	}
	for _, h := range m.Headers {
		if !validHeader(h.Key) || !validHeader(h.Value) || h.Key == "" {
			continue // header injection guard
		}
		msg.SetGenHeaderPreformatted(gomail.Header(h.Key), h.Value)
	}
	if m.Text != "" {
		msg.SetBodyString(gomail.TypeTextPlain, m.Text)
	}
	if m.HTML != "" {
		msg.AddAlternativeString(gomail.TypeTextHTML, m.HTML)
	}
	if m.Text == "" && m.HTML == "" {
		msg.SetBodyString(gomail.TypeTextPlain, " ")
	}
	if m.EnvelopeFrom == "" && p.EnvelopeSender != "" {
		m.EnvelopeFrom = p.EnvelopeSender
	}
	env := addressOnly(m.EnvelopeFrom)
	if env == "" {
		env = addressOnly(m.From)
	}
	if err := msg.EnvelopeFrom(env); err != nil {
		return fmt.Errorf("envelope sender %q: %w", env, err)
	}
	for _, a := range m.Attachments {
		name := path.Base(a.Name)
		if name == "" || name == "." || name == "/" {
			name = "attachment"
		}
		msg.AttachReader(name, strings.NewReader(string(a.Content)), gomail.WithFileContentType(gomail.ContentType(a.ContentType)))
	}

	client, err := gomail.NewClient(p.Host, opts...)
	if err != nil {
		return err
	}
	if err := client.DialAndSendWithContext(ctx, msg); err != nil {
		return fmt.Errorf("smtp %s:%d: %w", p.Host, port, err)
	}
	return nil
}

// messageIDLocal keeps the recipient id in the local part. Mail clients quote
// Message-ID when replying or forwarding, which is what the reporter uses.
func messageIDLocal(rid string) string {
	if rid == "" {
		return randomToken()
	}
	return rid + "." + randomToken()
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

func addressOnly(s string) string {
	if addr, err := mail.ParseAddress(s); err == nil {
		return addr.Address
	}
	return strings.Trim(strings.TrimSpace(s), "<>")
}

func displayName(s string) string {
	if addr, err := mail.ParseAddress(s); err == nil && addr.Name != "" {
		return addr.Name
	}
	return ""
}

func fromDomain(from string) string {
	addr := addressOnly(from)
	if i := strings.LastIndex(addr, "@"); i >= 0 && i+1 < len(addr) {
		return addr[i+1:]
	}
	return "localhost"
}

// validHeader rejects CR/LF so callers cannot inject SMTP headers.
func validHeader(v string) bool {
	return !strings.ContainsAny(v, "\r\n")
}
