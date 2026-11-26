package imapmon

import (
	"io"
	"regexp"
	"strings"

	gomessage "github.com/emersion/go-message/mail"
)

// ridPattern finds a tracking parameter in message bodies. Recipients usually
// report a phish by forwarding it, so the body still carries the tracked URL.
// The 3D prefix covers quoted-printable, and %3D covers URL-encoded links.
var ridPattern = regexp.MustCompile(`(?i)(?:\?|%3f)rid(?:=|%3d)(?:3d)?([A-Za-z0-9_-]{8,32})`)

const (
	maxScanBytes  = 8 << 20
	maxScanDepth  = 4
	maxTextToScan = 2 << 20
)

// ScanRIDs returns every tracking id found anywhere in a raw message, walking
// forwarded messages that arrive as message/rfc822 attachments.
func ScanRIDs(raw []byte) []string {
	found := map[string]bool{}
	if len(raw) > maxScanBytes {
		raw = raw[:maxScanBytes]
	}
	scanReader(strings.NewReader(string(raw)), 0, found)
	return orderedKeys(found)
}

func scanReader(rd io.Reader, depth int, found map[string]bool) {
	if depth > maxScanDepth {
		return
	}
	reader, err := gomessage.CreateReader(rd)
	if err != nil {
		// Not a MIME entity; scan it as raw text.
		scanText(rd, found)
		return
	}
	defer reader.Close()
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return
		}
		if err != nil {
			return
		}
		if part == nil {
			return
		}
		contentType := strings.ToLower(part.Header.Get("Content-Type"))
		if strings.Contains(contentType, "message/rfc822") {
			scanReader(part.Body, depth+1, found)
			continue
		}
		if strings.HasPrefix(contentType, "text/") || contentType == "" {
			scanText(part.Body, found)
		}
	}
}

func scanText(rd io.Reader, found map[string]bool) {
	raw, err := io.ReadAll(io.LimitReader(rd, maxTextToScan))
	if err != nil || len(raw) == 0 {
		return
	}
	for _, m := range ridPattern.FindAllSubmatch(raw, -1) {
		if len(m) > 1 && len(m[1]) > 0 {
			found[string(m[1])] = true
		}
	}
}

// orderedKeys makes scans deterministic for tests and logs.
func orderedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
