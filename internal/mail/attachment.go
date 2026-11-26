package mail

import (
	"archive/zip"
	"bytes"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Limits keep templating from turning into an archive bomb.
const (
	maxArchiveBytes = 25 << 20
	maxPartBytes    = 8 << 20
)

// textExtensions hold content that can be rendered as a template directly.
var textExtensions = map[string]bool{
	".txt": true, ".html": true, ".htm": true, ".ics": true,
	".csv": true, ".xml": true, ".json": true, ".md": true,
}

// archiveExtensions are zip containers whose XML parts can be rendered.
var archiveExtensions = map[string]bool{
	".docx": true, ".docm": true, ".pptx": true, ".xlsx": true, ".xlsm": true,
}

// RenderAttachment applies template placeholders inside an attachment, not just
// its filename. Plain text formats are rendered directly; Office documents have
// their XML parts rendered and the archive rebuilt. Anything else is returned
// untouched.
func RenderAttachment(name string, content []byte, render func(string) (string, error)) ([]byte, error) {
	ext := strings.ToLower(path.Ext(name))
	switch {
	case textExtensions[ext]:
		if !bytes.Contains(content, []byte("{{")) {
			return content, nil
		}
		out, err := render(string(content))
		if err != nil {
			return content, err
		}
		return []byte(out), nil
	case archiveExtensions[ext]:
		if !bytes.Contains(content, []byte("{{")) && !bytes.Contains(content, []byte("%7b%7b")) {
			return content, nil
		}
		return renderArchive(content, render)
	default:
		return content, nil
	}
}

// renderArchive rewrites the XML parts of an Office file. Word, Excel and
// PowerPoint URL-escape placeholders when they come from a hyperlink or a
// remote image, so %7b%7b.foo%7d%7d is decoded first.
var escapedPlaceholder = regexp.MustCompile(`(?i)%7b%7b\.([a-z]+)%7d%7d`)

func renderArchive(content []byte, render func(string) (string, error)) ([]byte, error) {
	if len(content) > maxArchiveBytes {
		return content, nil // too big to rewrite safely; send as-is
	}
	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return content, nil // not actually a zip; leave it alone
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			zw.Close()
			return content, err
		}
		raw, err := io.ReadAll(io.LimitReader(rc, maxPartBytes))
		rc.Close()
		if err != nil {
			zw.Close()
			return content, err
		}
		part := raw
		inner := strings.ToLower(path.Ext(f.Name))
		if inner == ".xml" || inner == ".rels" {
			text := escapedPlaceholder.ReplaceAllStringFunc(string(raw), func(m string) string {
				decoded, err := url.QueryUnescape(m)
				if err != nil {
					return m
				}
				return decoded
			})
			if strings.Contains(text, "{{") {
				rendered, err := render(text)
				if err != nil {
					zw.Close()
					return content, err
				}
				part = []byte(rendered)
			} else if text != string(raw) {
				part = []byte(text)
			}
		}
		w, err := zw.Create(f.Name)
		if err != nil {
			zw.Close()
			return content, err
		}
		if _, err := w.Write(part); err != nil {
			zw.Close()
			return content, err
		}
	}
	if err := zw.Close(); err != nil {
		return content, err
	}
	return buf.Bytes(), nil
}
