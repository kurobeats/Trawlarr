package api

import (
	"net/http"
	"time"
)

// PowerControl lets the UI stop or restart the process. It is wired by main;
// when nil (tests, embedded use) the endpoints report that they are
// unavailable rather than pretending to work.
type PowerControl struct {
	Shutdown func()
	Restart  func() error
}

// powerDelay gives the HTTP response time to flush before the process is
// replaced or stopped, so the browser sees the acknowledgement.
const powerDelay = 300 * time.Millisecond

// handlePowerAction stops or restarts the server. Admin only.
func (s *Server) handlePowerAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "shutdown" && action != "restart" {
		writeError(w, http.StatusBadRequest, "action must be shutdown or restart")
		return
	}
	if s.Power == nil || (action == "shutdown" && s.Power.Shutdown == nil) || (action == "restart" && s.Power.Restart == nil) {
		writeError(w, http.StatusNotImplemented, "power control is not available in this process")
		return
	}
	me := UserFrom(r)
	s.Store.Audit(me.OrgID, me.ID, "power."+action, me.Username, "", s.clientIP(r))

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "action": action})

	go func() {
		time.Sleep(powerDelay)
		if action == "shutdown" {
			s.Log.Info("shutdown requested from the UI", "user", me.Username)
			s.Power.Shutdown()
			return
		}
		s.Log.Info("restart requested from the UI", "user", me.Username)
		if err := s.Power.Restart(); err != nil {
			// Nothing else to do: let a supervisor with restart-on-failure
			// bring the service back.
			s.Log.Error("restart failed", "err", err)
		}
	}()
}
