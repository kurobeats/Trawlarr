package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Directory completion and inspection for the path fields in Settings.
//
// This mirrors the way Syncthing's browse endpoint works: the caller sends what
// has been typed so far and gets back a flat list of absolute directory paths
// that share that prefix. The UI feeds them to a datalist, so typing "/var/lo"
// previews every option starting with that string.

const (
	maxCompletions = 50
	maxEntriesScan = 5000
)

// handleFSComplete returns directory completions for a partially typed path.
func (s *Server) handleFSComplete(w http.ResponseWriter, r *http.Request) {
	typed := strings.TrimSpace(r.URL.Query().Get("path"))
	if strings.ContainsRune(typed, 0) {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}
	if typed == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"typed":   typed,
			"roots":   completionRoots(s),
			"matches": []string{},
		})
		return
	}

	// Split what was typed into the directory to scan and the name prefix.
	dir, prefix := filepath.Split(typed)
	if dir == "" {
		dir = "."
	}
	if home, err := os.UserHomeDir(); err == nil {
		if typed == "~" || strings.HasPrefix(typed, "~/") {
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
			typed = filepath.Join(home, strings.TrimPrefix(typed, "~/"))
			_, prefix = filepath.Split(typed)
			dir = strings.TrimSuffix(typed, prefix)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"typed": typed, "matches": []string{}, "error": humanPathError(err),
		})
		return
	}

	exact := []string{}
	folded := []string{}
	for i, entry := range entries {
		if i >= maxEntriesScan {
			break
		}
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		switch {
		case strings.HasPrefix(name, prefix):
			exact = append(exact, filepath.Join(dir, name)+string(filepath.Separator))
		case strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)):
			folded = append(folded, filepath.Join(dir, name)+string(filepath.Separator))
		}
	}
	sort.Strings(exact)
	sort.Strings(folded)
	matches := append(exact, folded...)
	truncated := false
	if len(matches) > maxCompletions {
		matches = matches[:maxCompletions]
		truncated = true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"typed": typed, "matches": matches, "truncated": truncated,
	})
}

// completionRoots are the sensible starting points when the field is empty.
func completionRoots(s *Server) []string {
	roots := []string{}
	if cwd, err := os.Getwd(); err == nil {
		roots = append(roots, cwd+string(filepath.Separator))
	}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, home+string(filepath.Separator))
	}
	if s.Cfg != nil {
		if data := s.Cfg.DataDir; data != "" {
			if abs, err := filepath.Abs(data); err == nil {
				roots = append(roots, abs+string(filepath.Separator))
			}
		}
	}
	return roots
}

// handleFSStat previews a path: what it resolves to and whether it is usable.
func (s *Server) handleFSStat(w http.ResponseWriter, r *http.Request) {
	typed := strings.TrimSpace(r.URL.Query().Get("path"))
	if typed == "" || strings.ContainsRune(typed, 0) {
		writeError(w, http.StatusBadRequest, "a path is required")
		return
	}
	out := map[string]any{"typed": typed}
	abs, err := filepath.Abs(typed)
	if err != nil {
		out["error"] = humanPathError(err)
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["resolved"] = abs

	info, statErr := os.Stat(abs)
	switch {
	case statErr == nil && info.IsDir():
		out["is_dir"] = true
		out["exists"] = true
		out["error"] = "this is a directory; a database file is expected"
		writeJSON(w, http.StatusOK, out)
		return
	case statErr == nil:
		out["exists"] = true
		out["size"] = info.Size()
		out["modified"] = info.ModTime().UTC().Format("2006-01-02T15:04:05Z")
	case errors.Is(statErr, os.ErrNotExist):
		out["exists"] = false
	default:
		out["error"] = humanPathError(statErr)
		writeJSON(w, http.StatusOK, out)
		return
	}

	// A new file needs a writable parent directory.
	parent := filepath.Dir(abs)
	parentInfo, parentErr := os.Stat(parent)
	switch {
	case parentErr != nil:
		out["parent_exists"] = false
		out["error"] = "the parent directory does not exist"
	case !parentInfo.IsDir():
		out["parent_exists"] = false
		out["error"] = "the parent path is not a directory"
	default:
		out["parent_exists"] = true
		out["parent_writable"] = writable(parent)
		if !writable(parent) {
			out["error"] = "the parent directory is not writable by this process"
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// writable reports whether this process can create files in dir.
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".trawlarr-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// humanPathError turns syscall errors into something worth showing.
func humanPathError(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "no such file or directory"
	case errors.Is(err, os.ErrPermission):
		return "permission denied"
	case errors.Is(err, os.ErrExist):
		return "already exists"
	}
	return err.Error()
}
