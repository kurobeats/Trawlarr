package mail

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kurobeats/trawlarr/internal/store"
)

// smtpSink is a minimal SMTP server that records the envelope and raw message.
type smtpSink struct {
	ln   net.Listener
	mu   sync.Mutex
	env  []string
	data []string
	port int
}

func newSMTPSink(t *testing.T) *smtpSink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &smtpSink{ln: ln, port: ln.Addr().(*net.TCPAddr).Port}
	t.Cleanup(func() { ln.Close() })
	go s.serve()
	return s
}

func (s *smtpSink) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *smtpSink) handle(conn net.Conn) {
	defer conn.Close()
	rd := bufio.NewReader(conn)
	wr := bufio.NewWriter(conn)
	say := func(line string) {
		fmt.Fprintf(wr, "%s\r\n", line)
		wr.Flush()
	}
	say("220 sink ESMTP")
	inData := false
	var body strings.Builder
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if inData {
			if trimmed == "." {
				inData = false
				s.mu.Lock()
				s.data = append(s.data, body.String())
				s.mu.Unlock()
				body.Reset()
				say("250 2.0.0 queued")
				continue
			}
			body.WriteString(trimmed + "\n")
			continue
		}
		upper := strings.ToUpper(trimmed)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			say("250-sink")
			say("250-8BITMIME")
			say("250 SIZE 10485760")
		case strings.HasPrefix(upper, "HELO"):
			say("250 sink")
		case strings.HasPrefix(upper, "MAIL FROM"), strings.HasPrefix(upper, "RCPT TO"):
			s.mu.Lock()
			s.env = append(s.env, trimmed)
			s.mu.Unlock()
			say("250 2.1.0 ok")
		case upper == "DATA":
			inData = true
			say("354 end with .")
		case strings.HasPrefix(upper, "QUIT"):
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (s *smtpSink) messages(t *testing.T) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := len(s.data)
		s.mu.Unlock()
		if n > 0 {
			s.mu.Lock()
			defer s.mu.Unlock()
			return append([]string(nil), s.data...)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no message arrived at the SMTP sink")
	return nil
}

func (s *smtpSink) envelope() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.env...)
}

func testProfile(port int) *store.Profile {
	return &store.Profile{Name: "sink", Host: "127.0.0.1", Port: port, FromAddress: `"IT Support" <it@example.com>`}
}

func send(t *testing.T, sink *smtpSink, msg Message, profile *store.Profile) string {
	t.Helper()
	s := &SMTP{Timeout: 5 * time.Second}
	if err := s.Send(context.Background(), profile, msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	return sink.messages(t)[0]
}

func TestSendBuildsExpectedMessage(t *testing.T) {
	sink := newSMTPSink(t)
	raw := send(t, sink, Message{
		To:           "alice@example.com",
		ToName:       "Alice Ng",
		From:         `"IT Support" <it@example.com>`,
		EnvelopeFrom: "bounce@example.com",
		Subject:      "Invoice ready",
		Text:         "hello",
		HTML:         "<p>hello</p>",
		Headers:      []store.Header{{Key: "X-Campaign", Value: "q4"}},
		RID:          "abcdefghijkl",
		Attachments:  []Attachment{{Name: "invoice.txt", ContentType: "text/plain", Content: []byte("payload")}},
	}, testProfile(sink.port))

	required := []string{
		"From: \"IT Support\" <it@example.com>",
		"To: \"Alice Ng\" <alice@example.com>",
		"Subject: Invoice ready",
		"X-Campaign: q4",
		"Message-ID: <abcdefghijkl.",
		"multipart/mixed",
		"multipart/alternative",
		`Content-Disposition: attachment; filename="invoice.txt"`,
		"cGF5bG9hZA==", // base64 of "payload"
	}
	for _, want := range required {
		if !strings.Contains(raw, want) {
			t.Errorf("message missing %q\n---\n%s", want, raw)
		}
	}
	// The envelope sender is separate from the header From.
	env := strings.Join(sink.envelope(), "\n")
	if !strings.Contains(strings.ToUpper(env), "MAIL FROM:<BOUNCE@EXAMPLE.COM>") {
		t.Fatalf("envelope sender not used: %s", env)
	}
}

func TestNoIdentifyingHeaders(t *testing.T) {
	sink := newSMTPSink(t)
	profile := testProfile(sink.port)
	raw := send(t, sink, Message{
		To: "alice@example.com", From: profile.FromAddress,
		Subject: "hi", Text: "hi",
	}, profile)
	lower := strings.ToLower(raw)
	for _, forbidden := range []string{"x-mailer", "trawlarr", "user-agent"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("outgoing mail contains %q:\n%s", forbidden, raw)
		}
	}
	// Message-ID must use the From domain, never the local hostname.
	line := headerLine(raw, "Message-ID")
	if !strings.HasSuffix(strings.TrimSpace(line), "@example.com>") {
		t.Fatalf("Message-ID leaks a non-sending domain: %q", line)
	}
}

func TestXMailerOnlyWhenRequested(t *testing.T) {
	sink := newSMTPSink(t)
	profile := testProfile(sink.port)
	raw := send(t, sink, Message{
		To: "alice@example.com", From: profile.FromAddress,
		Subject: "hi", Text: "hi", XMailer: "Outlook 16.0",
	}, profile)
	if !strings.Contains(raw, "X-Mailer: Outlook 16.0") {
		t.Fatalf("requested X-Mailer missing:\n%s", raw)
	}
}

func TestHeaderInjectionIsBlocked(t *testing.T) {
	sink := newSMTPSink(t)
	profile := testProfile(sink.port)
	raw := send(t, sink, Message{
		To: "alice@example.com", From: profile.FromAddress, Subject: "hi", Text: "hi",
		Headers: []store.Header{
			{Key: "X-Good", Value: "fine"},
			{Key: "X-Bad", Value: "value\r\nBcc: attacker@example.com"},
			{Key: "X-Bad-Key\r\nBcc", Value: "x"},
		},
	}, profile)
	if !strings.Contains(raw, "X-Good: fine") {
		t.Fatal("valid custom header dropped")
	}
	if strings.Contains(strings.ToLower(raw), "bcc: attacker@example.com") {
		t.Fatalf("header injection succeeded:\n%s", raw)
	}
}

func TestUnicodeSubjectAndRecipient(t *testing.T) {
	sink := newSMTPSink(t)
	profile := testProfile(sink.port)
	raw := send(t, sink, Message{
		To: "jürgen@example.com", ToName: "Jürgen Müller",
		From: profile.FromAddress, Subject: "Rechnung für März ✓", Text: "hallo",
	}, profile)
	if !strings.Contains(raw, "Subject: =?") {
		t.Fatalf("unicode subject not encoded:\n%s", raw)
	}
	if !strings.Contains(raw, "jürgen@example.com") {
		t.Fatal("recipient address mangled")
	}
}

func TestFromNameFallsBackToLocalPart(t *testing.T) {
	sink := newSMTPSink(t)
	profile := &store.Profile{Name: "sink", Host: "127.0.0.1", Port: sink.port, FromAddress: "it@example.com"}
	raw := send(t, sink, Message{To: "a@example.com", From: profile.FromAddress, Subject: "s", Text: "b"}, profile)
	if !strings.Contains(raw, "From: <it@example.com>") && !strings.Contains(raw, "From: it@example.com") {
		t.Fatalf("unexpected From header:\n%s", raw)
	}
}

func TestSendRejectsInvalidInput(t *testing.T) {
	s := &SMTP{}
	if err := s.Send(context.Background(), &store.Profile{Name: "nohost", FromAddress: "a@b.c"}, Message{To: "a@b.c", From: "a@b.c"}); err == nil {
		t.Fatal("profile without host accepted")
	}
	sink := newSMTPSink(t)
	profile := testProfile(sink.port)
	if err := s.Send(context.Background(), profile, Message{To: "not-an-address", From: profile.FromAddress}); err == nil {
		t.Fatal("invalid recipient accepted")
	}
	profile.FromAddress = "broken"
	if err := s.Send(context.Background(), profile, Message{To: "a@example.com", From: "broken"}); err == nil {
		t.Fatal("invalid from address accepted")
	}
}

func TestEmptyBodyStillSends(t *testing.T) {
	sink := newSMTPSink(t)
	profile := testProfile(sink.port)
	raw := send(t, sink, Message{To: "a@example.com", From: profile.FromAddress, Subject: "s"}, profile)
	if !strings.Contains(raw, "Content-Type: text/plain") {
		t.Fatalf("bodyless message has no text part:\n%s", raw)
	}
}

func TestAttachmentFilenameSanitised(t *testing.T) {
	sink := newSMTPSink(t)
	profile := testProfile(sink.port)
	raw := send(t, sink, Message{
		To: "a@example.com", From: profile.FromAddress, Subject: "s", Text: "b",
		Attachments: []Attachment{{Name: "../../etc/passwd", ContentType: "text/plain", Content: []byte("x")}},
	}, profile)
	if strings.Contains(raw, "etc/passwd") {
		t.Fatalf("path traversal survived in attachment name:\n%s", raw)
	}
}

func headerLine(raw, name string) string {
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(name)+":") {
			return line
		}
	}
	return ""
}

func TestTestMessageShape(t *testing.T) {
	m := TestMessage("a@example.com", "it@example.com")
	if m.To != "a@example.com" || m.From != "it@example.com" || m.Subject == "" || m.Text == "" {
		t.Fatalf("unexpected test message: %+v", m)
	}
}
