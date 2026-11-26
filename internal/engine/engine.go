// Package engine runs campaigns: it renders each recipient's email and hands
// it to the mailer at the configured rate, recording every outcome.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/kurobeats/trawlarr/internal/mail"
	"github.com/kurobeats/trawlarr/internal/notify"
	"github.com/kurobeats/trawlarr/internal/store"
	"github.com/kurobeats/trawlarr/internal/tmpl"
)

// Engine owns campaign execution.
type Engine struct {
	store   *store.Store
	sender  mail.Sender
	hub     *notify.Dispatcher
	log     *slog.Logger
	baseURL func(c *store.Campaign) string

	mu   sync.Mutex
	runs map[int64]context.CancelFunc
}

// New builds an engine. baseURL resolves the phish-server origin for a campaign.
func New(s *store.Store, sender mail.Sender, hub *notify.Dispatcher, log *slog.Logger, baseURL func(*store.Campaign) string) *Engine {
	return &Engine{
		store:   s,
		sender:  sender,
		hub:     hub,
		log:     log,
		baseURL: baseURL,
		runs:    map[int64]context.CancelFunc{},
	}
}

// Start resumes in-flight campaigns and then polls for scheduled ones.
func (e *Engine) Start(ctx context.Context) {
	sending, err := e.store.SendingCampaigns()
	if err != nil {
		e.log.Error("resume campaigns", "err", err)
	}
	for _, c := range sending {
		e.log.Info("resuming campaign", "campaign", c.Name, "id", c.ID)
		e.spawn(c)
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			due, err := e.store.DueCampaigns(time.Now())
			if err != nil {
				e.log.Error("schedule poll", "err", err)
				continue
			}
			for _, c := range due {
				if err := e.Launch(c); err != nil {
					e.log.Error("scheduled launch failed", "campaign", c.ID, "err", err)
					_ = e.store.SetCampaignStatus(c.ID, store.StatusError)
				}
			}
		}
	}
}

// Launch validates a campaign and starts (or schedules) it.
func (e *Engine) Launch(c *store.Campaign) error {
	if c.Status == store.StatusSending {
		return errors.New("campaign is already sending")
	}
	if len(c.GroupIDs) == 0 {
		return errors.New("campaign has no recipient groups")
	}
	if _, err := e.store.Template(c.OrgID, c.TemplateID); err != nil {
		return fmt.Errorf("template: %w", err)
	}
	if _, err := e.store.Profile(c.OrgID, c.ProfileID); err != nil {
		return fmt.Errorf("sending profile: %w", err)
	}
	if _, err := e.store.Page(c.OrgID, c.PageID); err != nil {
		return fmt.Errorf("landing page: %w", err)
	}
	n, err := e.store.MaterializeTargets(c)
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("no recipients (all may have opted out)")
	}
	if !c.LaunchAt.IsZero() && c.LaunchAt.Time().After(time.Now()) {
		return e.store.SetCampaignStatus(c.ID, store.StatusScheduled)
	}
	if err := e.store.SetCampaignStatus(c.ID, store.StatusQueued); err != nil {
		return err
	}
	e.spawn(c)
	return nil
}

// Stop cancels a running campaign.
func (e *Engine) Stop(campaignID int64) {
	e.mu.Lock()
	cancel, ok := e.runs[campaignID]
	e.mu.Unlock()
	if ok {
		cancel()
		return
	}
	_ = e.store.SetCampaignStatus(campaignID, store.StatusStopped)
}

// Running reports whether the engine currently owns a campaign goroutine.
func (e *Engine) Running(campaignID int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.runs[campaignID]
	return ok
}

func (e *Engine) spawn(c *store.Campaign) {
	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	if old, ok := e.runs[c.ID]; ok {
		old()
	}
	e.runs[c.ID] = cancel
	e.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			e.mu.Lock()
			delete(e.runs, c.ID)
			e.mu.Unlock()
		}()
		e.run(ctx, c)
	}()
}

func (e *Engine) run(ctx context.Context, c *store.Campaign) {
	log := e.log.With("campaign", c.ID, "name", c.Name)
	if err := e.store.SetCampaignStatus(c.ID, store.StatusSending); err != nil {
		log.Error("set status", "err", err)
		return
	}
	// Re-read so started_at and status reflect reality.
	if fresh, err := e.store.Campaign(c.OrgID, c.ID); err == nil {
		c = fresh
	}
	e.store.AddEvent(c.ID, "", store.EvCampaignStarted, "Campaign started")
	e.emit(c, store.EvCampaignStarted, "", "", "", nil)

	t, err := e.store.Template(c.OrgID, c.TemplateID)
	if err != nil {
		log.Error("load template", "err", err)
		e.fail(c, err)
		return
	}
	profile, err := e.store.Profile(c.OrgID, c.ProfileID)
	if err != nil {
		log.Error("load profile", "err", err)
		e.fail(c, err)
		return
	}
	pending, err := e.store.PendingTargets(c.ID)
	if err != nil {
		log.Error("load targets", "err", err)
		e.fail(c, err)
		return
	}
	var interval time.Duration
	if c.SendRate > 0 {
		interval = time.Hour / time.Duration(c.SendRate)
	}

	for _, target := range pending {
		select {
		case <-ctx.Done():
			e.store.AddEvent(c.ID, "", store.EvCampaignStopped, "Campaign stopped")
			_ = e.store.SetCampaignStatus(c.ID, store.StatusStopped)
			e.emit(c, store.EvCampaignStopped, "", "", "", nil)
			return
		default:
		}
		if !c.SendBy.IsZero() && time.Now().After(c.SendBy.Time()) {
			e.store.AddEvent(c.ID, "", store.EvCampaignStopped, "Send-by date reached")
			break
		}
		msg, err := e.render(c, t, profile, target)
		if err != nil {
			log.Warn("render failed", "email", target.Email, "err", err)
			e.recordFailure(c, target, err)
			continue
		}
		if err := e.send(ctx, profile, msg); err != nil {
			log.Warn("send failed", "email", target.Email, "err", err)
			if isUncertain(err) {
				e.recordUncertain(c, target, err)
			} else {
				e.recordFailure(c, target, err)
			}
		} else {
			if err := e.store.MarkSent(target.ID, ""); err != nil {
				log.Error("mark sent", "err", err)
			}
			e.store.AddEvent(c.ID, target.RID, store.EvEmailSent, "Email sent to "+target.Email)
			e.emit(c, store.EvEmailSent, target.Email, target.RID, "", nil)
		}
		sleep(ctx, interval, c.Jitter)
	}

	e.finish(c)
}

func (e *Engine) finish(c *store.Campaign) {
	if remaining, err := e.store.PendingTargets(c.ID); err == nil && len(remaining) == 0 {
		_ = e.store.SetCampaignStatus(c.ID, store.StatusCompleted)
		e.store.AddEvent(c.ID, "", store.EvCampaignCompleted, "Campaign completed")
		e.emit(c, store.EvCampaignCompleted, "", "", "", nil)
		return
	}
	_ = e.store.SetCampaignStatus(c.ID, store.StatusCompleted)
	e.store.AddEvent(c.ID, "", store.EvCampaignCompleted, "Campaign finished with pending recipients")
	e.emit(c, store.EvCampaignCompleted, "", "", "", nil)
}

func (e *Engine) fail(c *store.Campaign, err error) {
	_ = e.store.SetCampaignStatus(c.ID, store.StatusError)
	e.store.AddEvent(c.ID, "", store.EvCampaignStopped, "Campaign error: "+err.Error())
	e.emit(c, store.EvCampaignStopped, "", "", err.Error(), nil)
}

func (e *Engine) send(ctx context.Context, p *store.Profile, msg mail.Message) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if !sleep(ctx, time.Second*time.Duration(attempt), 0) {
				return ctx.Err()
			}
		}
		if err := e.sender.Send(ctx, p, msg); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func (e *Engine) render(c *store.Campaign, t *store.Template, p *store.Profile, target store.CampaignTarget) (mail.Message, error) {
	ctxData, err := tmpl.NewContext(p.FromAddress, e.baseURL(c), target.RID, tmpl.Recipient{
		FirstName: target.FirstName,
		LastName:  target.LastName,
		Email:     target.Email,
		Position:  target.Position,
		Custom:    target.Custom,
	})
	if err != nil {
		return mail.Message{}, err
	}
	subject, err := tmpl.Execute(t.Subject, ctxData)
	if err != nil {
		return mail.Message{}, fmt.Errorf("subject: %w", err)
	}
	text, err := tmpl.Execute(t.TextBody, ctxData)
	if err != nil {
		return mail.Message{}, fmt.Errorf("text body: %w", err)
	}
	html, err := tmpl.Execute(t.HTMLBody, ctxData)
	if err != nil {
		return mail.Message{}, fmt.Errorf("html body: %w", err)
	}
	msg := mail.Message{
		To:           target.Email,
		ToName:       target.FirstName + " " + target.LastName,
		From:         p.FromAddress,
		EnvelopeFrom: p.EnvelopeSender,
		Subject:      subject,
		Text:         text,
		HTML:         html,
		Headers:      p.Headers,
		RID:          target.RID,
	}
	for _, id := range t.Attachments {
		name, ctype, content, err := e.store.AttachmentContent(t.OrgID, id)
		if err != nil {
			continue
		}
		// Attachment names may be templated too.
		if rendered, err := tmpl.Execute(name, ctxData); err == nil && rendered != "" {
			name = rendered
		}
		msg.Attachments = append(msg.Attachments, mail.Attachment{Name: name, ContentType: ctype, Content: content})
	}
	return msg, nil
}

// isUncertain reports whether a failure happened after the message may have
// been accepted. Resending these blindly would double-send to real people.
func isUncertain(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, definite := range []string{"connection refused", "no such host", "authentication", "535", "530", "550", "554"} {
		if strings.Contains(msg, definite) {
			return false
		}
	}
	for _, maybe := range []string{"timeout", "unexpected eof", "connection reset", "broken pipe", "i/o timeout"} {
		if strings.Contains(msg, maybe) {
			return true
		}
	}
	return false
}

func (e *Engine) recordUncertain(c *store.Campaign, target store.CampaignTarget, err error) {
	_ = e.store.MarkUncertain(target.ID, err.Error())
	e.store.AddEvent(c.ID, target.RID, store.EvEmailFailed, "Delivery uncertain for "+target.Email+": "+err.Error())
	e.emit(c, store.EvEmailFailed, target.Email, target.RID, "uncertain: "+err.Error(), nil)
}

func (e *Engine) recordFailure(c *store.Campaign, target store.CampaignTarget, err error) {
	_ = e.store.MarkFailed(target.ID, err.Error())
	e.store.AddEvent(c.ID, target.RID, store.EvEmailFailed, "Error sending to "+target.Email+": "+err.Error())
	e.emit(c, store.EvEmailFailed, target.Email, target.RID, err.Error(), nil)
}

func (e *Engine) emit(c *store.Campaign, event, recipient, rid, message string, data map[string]any) {
	e.hub.Emit(c.OrgID, notify.Payload{
		Event:      event,
		CampaignID: c.ID,
		Campaign:   c.Name,
		Recipient:  recipient,
		RID:        rid,
		Message:    message,
		Data:       data,
		At:         time.Now().UTC(),
	})
}

// sleep waits for interval plus up to jitterSeconds of random delay.
// It returns false if the context was cancelled first.
func sleep(ctx context.Context, interval time.Duration, jitterSeconds int) bool {
	d := interval
	if jitterSeconds > 0 {
		d += time.Duration(rand.Intn(jitterSeconds)) * time.Second
	}
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
