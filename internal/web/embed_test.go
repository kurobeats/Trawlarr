package web

import (
	"io"
	"io/fs"
	"testing"
)

func TestDistContainsEntryPoint(t *testing.T) {
	fsys, err := Dist()
	if err != nil {
		t.Fatalf("embedded UI is missing: %v", err)
	}
	f, err := fsys.Open("index.html")
	if err != nil {
		t.Fatalf("index.html not embedded: %v", err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 {
		t.Fatal("index.html is empty")
	}
	if string(body) != "" && !contains(string(body), "root") {
		t.Logf("index.html looks like the placeholder build: %.120s", body)
	}
}

func TestDistHasAssets(t *testing.T) {
	fsys, err := Dist()
	if err != nil {
		t.Fatal(err)
	}
	matches, err := fs.Glob(fsys, "assets/*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		// A fresh checkout ships the placeholder build with no assets; that is
		// acceptable as long as index.html exists.
		t.Skip("no assets directory; run make ui to build the bundle")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
