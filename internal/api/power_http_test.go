package api

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type powerCalls struct {
	shutdown int32
	restart  int32
}

func (p *powerCalls) control() *PowerControl {
	return &PowerControl{
		Shutdown: func() { atomic.AddInt32(&p.shutdown, 1) },
		Restart:  func() error { atomic.AddInt32(&p.restart, 1); return nil },
	}
}

func waitFor(t *testing.T, fn func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not happen", what)
}

func TestPowerActions(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	calls := &powerCalls{}
	h.srv.Power = calls.control()

	status, body := h.do(c, http.MethodPost, h.admin.URL+"/api/v1/power/restart", nil)
	if status != http.StatusAccepted {
		t.Fatalf("restart = %d: %s", status, body)
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&calls.restart) == 1 }, "restart")

	status, body = h.do(c, http.MethodPost, h.admin.URL+"/api/v1/power/shutdown", nil)
	if status != http.StatusAccepted {
		t.Fatalf("shutdown = %d: %s", status, body)
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&calls.shutdown) == 1 }, "shutdown")

	if atomic.LoadInt32(&calls.restart) != 1 || atomic.LoadInt32(&calls.shutdown) != 1 {
		t.Fatalf("unexpected calls: restart=%d shutdown=%d", calls.restart, calls.shutdown)
	}
}

func TestPowerActionAudit(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.srv.Power = (&powerCalls{}).control()
	h.expect(c, http.MethodPost, "/api/v1/power/restart", nil, http.StatusAccepted)
	raw := h.expectRaw(c, http.MethodGet, "/api/v1/audit", nil, http.StatusOK)
	if !strings.Contains(string(raw), "power.restart") {
		t.Fatalf("power action not audited: %s", raw)
	}
}

func TestPowerActionPermissions(t *testing.T) {
	h := newHarness(t)
	admin := h.login()
	calls := &powerCalls{}
	h.srv.Power = calls.control()

	h.expect(admin, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "op", "password": "operator-password-1", "role": "operator",
	}, http.StatusCreated)
	op := h.client()
	h.do(op, http.MethodPost, h.admin.URL+"/api/v1/login",
		map[string]string{"username": "op", "password": "operator-password-1"})

	for _, action := range []string{"shutdown", "restart"} {
		if status, _ := h.do(op, http.MethodPost, h.admin.URL+"/api/v1/power/"+action, nil); status != http.StatusForbidden {
			t.Errorf("operator %s = %d, want 403", action, status)
		}
		if status, _ := h.do(h.client(), http.MethodPost, h.admin.URL+"/api/v1/power/"+action, nil); status != http.StatusUnauthorized {
			t.Errorf("anonymous %s = %d, want 401", action, status)
		}
	}
	time.Sleep(powerDelay + 100*time.Millisecond)
	if atomic.LoadInt32(&calls.shutdown) != 0 || atomic.LoadInt32(&calls.restart) != 0 {
		t.Fatal("a refused request still triggered a power action")
	}
}

func TestPowerActionValidation(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.srv.Power = (&powerCalls{}).control()
	h.expect(c, http.MethodPost, "/api/v1/power/explode", nil, http.StatusBadRequest)

	// Without a wired control the endpoint says so instead of doing nothing.
	h.srv.Power = nil
	h.expect(c, http.MethodPost, "/api/v1/power/shutdown", nil, http.StatusNotImplemented)
}
