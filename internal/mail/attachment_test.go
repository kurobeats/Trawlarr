package mail

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func renderer(t *testing.T) func(string) (string, error) {
	t.Helper()
	return func(s string) (string, error) {
		return strings.NewReplacer(
			"{{.FirstName}}", "Alice",
			"{{.URL}}", "https://phish.test/click?rid=abc",
		).Replace(s), nil
	}
}

func TestTextAttachmentIsTemplated(t *testing.T) {
	in := []byte("Hi {{.FirstName}}, open {{.URL}}")
	out, err := RenderAttachment("note.txt", in, renderer(t))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "Hi Alice, open https://phish.test/click?rid=abc" {
		t.Fatalf("rendered = %q", out)
	}
}

func TestBinaryAttachmentIsUntouched(t *testing.T) {
	in := append([]byte{0x00, 0x01}, []byte("{{.FirstName}}")...)
	in = append(in, 0xff)
	out, err := RenderAttachment("blob.bin", in, renderer(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, out) {
		t.Fatal("binary attachment was modified")
	}
}

func TestAttachmentWithoutPlaceholdersIsUntouched(t *testing.T) {
	in := []byte("no placeholders here")
	out, _ := RenderAttachment("plain.txt", in, renderer(t))
	if string(out) != string(in) {
		t.Fatalf("changed: %q", out)
	}
}

func TestOfficeArchiveIsRewritten(t *testing.T) {
	doc := buildZip(t, map[string]string{
		"word/document.xml":            `<w:t>Hi {{.FirstName}}</w:t>`,
		"word/media/image1.png":        "\x89PNG binary",
		"word/_rels/document.xml.rels": "{{.URL}}",
	})
	out, err := RenderAttachment("invoice.docx", doc, renderer(t))
	if err != nil {
		t.Fatal(err)
	}
	entries := readZip(t, out)
	if !strings.Contains(entries["word/document.xml"], "Hi Alice") {
		t.Fatalf("document.xml not rendered: %q", entries["word/document.xml"])
	}
	if !strings.Contains(entries["word/_rels/document.xml.rels"], "https://phish.test/click") {
		t.Fatalf("rels not rendered: %q", entries["word/_rels/document.xml.rels"])
	}
	if entries["word/media/image1.png"] != "\x89PNG binary" {
		t.Fatal("binary part was modified")
	}
}

func TestOfficeArchiveDecodesEscapedPlaceholders(t *testing.T) {
	doc := buildZip(t, map[string]string{
		"word/_rels/document.xml.rels": `Target="%7b%7b.URL%7d%7d"`,
	})
	out, err := RenderAttachment("invoice.docx", doc, renderer(t))
	if err != nil {
		t.Fatal(err)
	}
	entries := readZip(t, out)
	if !strings.Contains(entries["word/_rels/document.xml.rels"], "https://phish.test/click") {
		t.Fatalf("escaped placeholder not handled: %q", entries["word/_rels/document.xml.rels"])
	}
}

func TestBrokenArchiveIsSentUnchanged(t *testing.T) {
	in := []byte("PK this is not really a zip {{.FirstName}}")
	out, err := RenderAttachment("broken.docx", in, renderer(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(in, out) {
		t.Fatal("broken archive was altered")
	}
}

func TestRenderErrorIsReturned(t *testing.T) {
	_, err := RenderAttachment("note.txt", []byte("{{.FirstName}}"), func(string) (string, error) {
		return "", io.ErrUnexpectedEOF
	})
	if err == nil {
		t.Fatal("render error was swallowed")
	}
}

func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readZip(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = string(content)
	}
	return out
}
