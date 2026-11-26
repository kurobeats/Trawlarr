package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kurobeats/trawlarr/internal/db"
	"github.com/kurobeats/trawlarr/internal/secret"
	"github.com/kurobeats/trawlarr/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	conn, err := db.Open(t.TempDir() + "/notify.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	box, _ := secret.New(make([]byte, 32))
	st := store.New(conn, box)
	if _, err := st.Seed("admin", "hash"); err != nil {
		t.Fatal(err)
	}
	return st
}

type received struct {
	mu     sync.Mutex
	body   []byte
	event  string
	sig    string
	count  int
	status int
}

func (r *received) handler(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.body = body
	r.event = req.Header.Get("X-Webhook-Event")
	r.sig = req.Header.Get("X-Webhook-Signature")
	r.count++
	status := r.status
	r.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
}

// waitFor blocks until the handler has seen at least n requests.
func (r *received) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := r.count
		r.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.mu.Lock()
	got := r.count
	r.mu.Unlock()
	t.Fatalf("expected %d deliveries, saw %d", n, got)
}

func newDispatcher(t *testing.T, st *store.Store) *Dispatcher {
	t.Helper()
	old := retryBase
	retryBase = 5 * time.Millisecond
	t.Cleanup(func() { retryBase = old })
	return NewDispatcher(st, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
}

func TestEmitSignsPayload(t *testing.T) {
	st := newStore(t)
	if err := st.SaveWebhook(&store.Webhook{OrgID: 1, Name: "siem", URL: "placeholder", Secret: "s3cret", Events: []string{"*"}, Enabled: true}, 0); err != nil {
		t.Fatal(err)
	}
	rec := &received{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()
	hook, _ := st.Webhook(1, 1)
	hook.URL = srv.URL
	if err := st.SaveWebhook(hook, hook.ID); err != nil {
		t.Fatal(err)
	}

	d := newDispatcher(t, st)
	d.Emit(1, Payload{Event: "email.opened", CampaignID: 7, Campaign: "Q4", Recipient: "a@example.com"})
	rec.waitFor(t, 1)

	rec.mu.Lock()
	body, event, sig := rec.body, rec.event, rec.sig
	rec.mu.Unlock()

	if event != "email.opened" {
		t.Fatalf("event header = %q", event)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if sig != want {
		t.Fatalf("signature mismatch:\n got %s\nwant %s", sig, want)
	}
	if !contains(string(body), `"campaign_id":7`) {
		t.Fatalf("payload missing campaign id: %s", body)
	}
}

func TestEmitRetriesOnServerError(t *testing.T) {
	st := newStore(t)
	rec := &received{status: http.StatusInternalServerError}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()
	if err := st.SaveWebhook(&store.Webhook{OrgID: 1, Name: "flaky", URL: srv.URL, Events: []string{"*"}, Enabled: true}, 0); err != nil {
		t.Fatal(err)
	}
	d := newDispatcher(t, st)
	d.Emit(1, Payload{Event: "email.sent", CampaignID: 1})
	rec.waitFor(t, 3) // one attempt plus two retries
}

func TestEmitFiltersEvents(t *testing.T) {
	st := newStore(t)
	rec := &received{}
	calls := int32(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		rec.handler(w, r)
	}))
	defer srv.Close()
	if err := st.SaveWebhook(&store.Webhook{OrgID: 1, Name: "opens", URL: srv.URL, Events: []string{"email.opened"}, Enabled: true}, 0); err != nil {
		t.Fatal(err)
	}
	d := newDispatcher(t, st)
	d.Emit(1, Payload{Event: "link.clicked", CampaignID: 1})
	time.Sleep(200 * time.Millisecond)
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("webhook fired for an event it did not subscribe to")
	}
	d.Emit(1, Payload{Event: "email.opened", CampaignID: 1})
	rec.waitFor(t, 1)
}

func TestDisabledWebhooksAreSkipped(t *testing.T) {
	st := newStore(t)
	calls := int32(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer srv.Close()
	if err := st.SaveWebhook(&store.Webhook{OrgID: 1, Name: "off", URL: srv.URL, Events: []string{"*"}, Enabled: false}, 0); err != nil {
		t.Fatal(err)
	}
	newDispatcher(t, st).Emit(1, Payload{Event: "email.sent", CampaignID: 1})
	time.Sleep(150 * time.Millisecond)
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("disabled webhook was called")
	}
}

func TestEmitWithNilDispatcherIsSafe(t *testing.T) {
	var d *Dispatcher
	d.Emit(1, Payload{Event: "email.sent"}) // must not panic
}

func TestQueueFullIsDropped(t *testing.T) {
	st := newStore(t)
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)
	if err := st.SaveWebhook(&store.Webhook{OrgID: 1, Name: "slow", URL: srv.URL, Events: []string{"*"}, Enabled: true}, 0); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(st, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	// Fill the queue without blocking the caller.
	for i := 0; i < 300; i++ {
		d.Emit(1, Payload{Event: "email.sent", CampaignID: int64(i)})
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
