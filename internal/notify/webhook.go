// Package notify delivers webhook payloads with HMAC signatures and retries.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/kurobeats/trawlarr/internal/store"
)

// Payload is the JSON body posted to webhooks.
type Payload struct {
	Event      string         `json:"event"`
	CampaignID int64          `json:"campaign_id"`
	Campaign   string         `json:"campaign"`
	Recipient  string         `json:"recipient,omitempty"`
	RID        string         `json:"rid,omitempty"`
	Message    string         `json:"message,omitempty"`
	IP         string         `json:"ip,omitempty"`
	Data       any            `json:"data,omitempty"`
	At         time.Time      `json:"at"`
}

// Dispatcher fans events out to webhooks without blocking the caller.
type Dispatcher struct {
	store  *store.Store
	client *http.Client
	log    *slog.Logger
	queue  chan job
}

// retryBase is the first backoff step. Tests shorten it.
var retryBase = time.Second

type job struct {
	hook    *store.Webhook
	payload Payload
}

// NewDispatcher starts n background delivery workers.
func NewDispatcher(s *store.Store, log *slog.Logger, workers int) *Dispatcher {
	if workers <= 0 {
		workers = 2
	}
	d := &Dispatcher{
		store:  s,
		client: &http.Client{Timeout: 10 * time.Second},
		log:    log,
		queue:  make(chan job, 256),
	}
	for i := 0; i < workers; i++ {
		go d.worker()
	}
	return d
}

// Emit queues payloads for every webhook subscribed to the event.
func (d *Dispatcher) Emit(orgID int64, p Payload) {
	if d == nil {
		return
	}
	if p.At.IsZero() {
		p.At = time.Now().UTC()
	}
	hooks, err := d.store.WebhooksForEvent(orgID, p.Event)
	if err != nil {
		d.log.Warn("webhook lookup failed", "err", err)
		return
	}
	for _, h := range hooks {
		select {
		case d.queue <- job{hook: h, payload: p}:
		default:
			d.log.Warn("webhook queue full, dropping event", "event", p.Event, "url", h.URL)
		}
	}
}

func (d *Dispatcher) worker() {
	for j := range d.queue {
		d.deliver(j)
	}
}

func (d *Dispatcher) deliver(j job) {
	body, err := json.Marshal(j.payload)
	if err != nil {
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(retryBase * time.Duration(1<<attempt))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.hook.URL, bytes.NewReader(body))
		if err != nil {
			cancel()
			d.log.Warn("webhook request build failed", "url", j.hook.URL, "err", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "webhook/1")
		req.Header.Set("X-Webhook-Event", j.payload.Event)
		if j.hook.Secret != "" {
			mac := hmac.New(sha256.New, []byte(j.hook.Secret))
			mac.Write(body)
			req.Header.Set("X-Webhook-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
		resp, err := d.client.Do(req)
		cancel()
		if err != nil {
			d.log.Warn("webhook delivery failed", "url", j.hook.URL, "attempt", attempt+1, "err", err)
			continue
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode < 300 {
			return
		}
		d.log.Warn("webhook non-2xx", "url", j.hook.URL, "status", resp.StatusCode, "attempt", attempt+1)
	}
}
