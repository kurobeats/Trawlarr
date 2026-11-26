package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kurobeats/trawlarr/internal/geo"
)

// geoipDownload tracks an in-flight database download so the UI can poll.
type geoipDownload struct {
	mu      sync.Mutex
	running bool
	message string
	url     string
	err     string
	doneAt  time.Time
}

func (s *Server) geoStatus() map[string]any {
	out := map[string]any{"available": false}
	if s.Geo == nil {
		return out
	}
	out["available"] = s.Geo.Available()
	out["database"] = s.Geo.Info()
	s.geoDL.mu.Lock()
	out["download"] = map[string]any{
		"running": s.geoDL.running,
		"message": s.geoDL.message,
		"url":     s.geoDL.url,
		"error":   s.geoDL.err,
	}
	s.geoDL.mu.Unlock()
	return out
}

// handleGetGeoIP reports the installed database and any download in progress.
func (s *Server) handleGetGeoIP(w http.ResponseWriter, r *http.Request) {
	if s.Geo == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "database": nil,
			"hint": "GeoIP is not wired up in this build"})
		return
	}
	writeJSON(w, http.StatusOK, s.geoStatus())
}

// handleUploadGeoIP installs a database the operator supplies (.mmdb or .mmdb.gz).
func (s *Server) handleUploadGeoIP(w http.ResponseWriter, r *http.Request) {
	if s.Geo == nil {
		writeError(w, http.StatusNotImplemented, "GeoIP is not enabled")
		return
	}
	me := UserFrom(r)
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart 'file' field")
		return
	}
	defer file.Close()
	var src io.Reader = file
	if strings.HasSuffix(strings.ToLower(header.Filename), ".gz") {
		gz, err := gzipReader(file)
		if err != nil {
			writeError(w, http.StatusBadRequest, "file is not a valid gzip archive")
			return
		}
		defer gz.Close()
		src = gz
	}
	if err := s.Geo.Install(src); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "geoip.uploaded", header.Filename, "", s.clientIP(r))
	writeJSON(w, http.StatusOK, s.geoStatus())
}

// handleDownloadGeoIP fetches the free DB-IP Lite city database in the
// background and reports progress through the status endpoint.
func (s *Server) handleDownloadGeoIP(w http.ResponseWriter, r *http.Request) {
	if s.Geo == nil {
		writeError(w, http.StatusNotImplemented, "GeoIP is not enabled")
		return
	}
	me := UserFrom(r)
	s.geoDL.mu.Lock()
	if s.geoDL.running {
		s.geoDL.mu.Unlock()
		writeError(w, http.StatusConflict, "a database download is already running")
		return
	}
	s.geoDL.running = true
	s.geoDL.err = ""
	s.geoDL.message = "starting download"
	s.geoDL.url = ""
	s.geoDL.mu.Unlock()

	s.Store.Audit(me.OrgID, me.ID, "geoip.downloaded", "db-ip-lite", "", s.clientIP(r))

	go func() {
		// A database is ~100MB uncompressed; give it room but not forever.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		url, err := s.Geo.Download(ctx)
		s.geoDL.mu.Lock()
		s.geoDL.running = false
		s.geoDL.doneAt = time.Now()
		if err != nil {
			s.geoDL.err = err.Error()
			s.geoDL.message = "download failed"
		} else {
			s.geoDL.url = url
			s.geoDL.message = "installed"
		}
		s.geoDL.mu.Unlock()
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

// handleGeoIPLookup lets the operator check one address from the UI.
func (s *Server) handleGeoIPLookup(w http.ResponseWriter, r *http.Request) {
	if s.Geo == nil {
		writeError(w, http.StatusNotImplemented, "GeoIP is not enabled")
		return
	}
	var req struct {
		IP string `json:"ip"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	loc, ok := s.Geo.Lookup(strings.TrimSpace(req.IP))
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"found": false, "ip": req.IP})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"found": true, "ip": req.IP, "location": loc})
}

// gzipReader is a small seam so the upload handler stays testable.
func gzipReader(rd interface{ Read([]byte) (int, error) }) (ioReadCloser, error) {
	return newGzipReader(rd)
}

var _ = geo.Location{}
