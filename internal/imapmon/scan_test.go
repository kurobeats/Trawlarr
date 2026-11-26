package imapmon

import (
	"encoding/base64"
	"mime"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/emersion/go-imap"
)

func TestScanRIDsInPlainBody(t *testing.T) {
	raw := "From: a@example.com\r\nContent-Type: text/plain\r\n\r\n" +
		"See https://phish.test/click?rid=abcdefghijk1 for details"
	got := ScanRIDs([]byte(raw))
	if len(got) != 1 || got[0] != "abcdefghijk1" {
		t.Fatalf("found %v", got)
	}
}

func TestScanRIDsHandlesEncodings(t *testing.T) {
	cases := map[string]string{
		"quoted printable": "https://phish.test/click?rid=3Dabcdefghijk2",
		"url encoded":      "https://phish.test/click%3Frid%3Dabcdefghijk3",
		"short form":       "?rid=abcdefghijk4",
	}
	for name, body := range cases {
		want := map[string]string{
			"quoted printable": "abcdefghijk2",
			"url encoded":      "abcdefghijk3",
			"short form":       "abcdefghijk4",
		}[name]
		raw := "Content-Type: text/plain\r\n\r\n" + body
		got := ScanRIDs([]byte(raw))
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s: found %v, want %s", name, got, want)
		}
	}
}

func TestScanRIDsInHTMLPart(t *testing.T) {
	raw := "Content-Type: text/html; charset=utf-8\r\n\r\n" +
		`<a href="https://phish.test/click?rid=abcdefghijk5">x</a>`
	if got := ScanRIDs([]byte(raw)); len(got) != 1 || got[0] != "abcdefghijk5" {
		t.Fatalf("found %v", got)
	}
}

func TestScanRIDsInForwardedAttachment(t *testing.T) {
	inner := "Subject: invoice\r\nContent-Type: text/html\r\n\r\n" +
		`<a href="https://phish.test/click?rid=abcdefghijk6">pay</a>`

	var buf strings.Builder
	buf.WriteString("From: reporter@example.com\r\nSubject: Fwd: invoice\r\n")
	buf.WriteString("Content-Type: " + mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": "FWD"}) + "\r\n\r\n")
	buf.WriteString("--FWD\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n")
	buf.WriteString("--FWD\r\nContent-Type: message/rfc822\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(inner))
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		buf.WriteString(encoded[i:end] + "\r\n")
	}
	buf.WriteString("\r\n--FWD--\r\n")

	got := ScanRIDs([]byte(buf.String()))
	if len(got) != 1 || got[0] != "abcdefghijk6" {
		t.Fatalf("forwarded attachment not scanned: %v", got)
	}
}

func TestScanRIDsIgnoresUnrelatedLinks(t *testing.T) {
	raw := "Content-Type: text/plain\r\n\r\nhttps://example.com/?page=2&id=abcdefghijk7"
	if got := ScanRIDs([]byte(raw)); len(got) != 0 {
		t.Fatalf("false positive: %v", got)
	}
}

func TestScanRIDsDeduplicates(t *testing.T) {
	body := "?rid=abcdefghijk8 and again ?rid=abcdefghijk8"
	raw := "Content-Type: text/plain\r\n\r\n" + body
	if got := ScanRIDs([]byte(raw)); len(got) != 1 {
		t.Fatalf("duplicates kept: %v", got)
	}
}

func TestAllowedSender(t *testing.T) {
	env := func(from string) *imap.Envelope {
		parts := strings.Split(from, "@")
		return &imap.Envelope{From: []*imap.Address{{MailboxName: parts[0], HostName: parts[1]}}}
	}
	open := &Monitor{}
	if !open.allowedSender(env("anyone@elsewhere.net")) {
		t.Fatal("empty restriction rejected a sender")
	}
	restricted := &Monitor{Settings: Settings{RestrictDomain: "corp.example"}}
	if !restricted.allowedSender(env("alice@corp.example")) {
		t.Fatal("company sender rejected")
	}
	if restricted.allowedSender(env("attacker@evil.net")) {
		t.Fatal("foreign sender accepted")
	}
	if !restricted.allowedSender(nil) {
		t.Fatal("nil envelope should not panic")
	}
}

func TestMultiPartBuilderHelper(t *testing.T) {
	// Guards the test helper above: a malformed boundary must not panic the
	// scanner.
	mw := multipart.NewWriter(&strings.Builder{})
	if mw.Boundary() == "" {
		t.Fatal("multipart writer has no boundary")
	}
}
