// Package imapmon watches a mailbox for messages that recipients reported as
// phishing and marks the matching campaign recipient.
//
// Matching uses the recipient id embedded in the Message-ID (and in
// References/In-Reply-To when the message was forwarded), so no identifying
// header has to be added to outgoing mail.
package imapmon

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"

	"github.com/kurobeats/trawlarr/internal/config"
	"github.com/kurobeats/trawlarr/internal/notify"
	"github.com/kurobeats/trawlarr/internal/store"
)

// Settings describes the report mailbox.
type Settings struct {
	Host           string
	Port           int
	Username       string
	Password       string
	Folder         string
	UseTLS         bool
	IgnoreCert     bool
	Delete         bool
	RestrictDomain string
	Interval       time.Duration
}

// Enabled reports whether a mailbox is configured.
func (s Settings) Enabled() bool { return s.Host != "" && s.Username != "" }

// Load merges config.json values with anything an imported database carried.
func Load(st *store.Store, cfg *config.Config) Settings {
	s := Settings{
		Host:           cfg.IMAP.Host,
		Port:           cfg.IMAP.Port,
		Username:       cfg.IMAP.Username,
		Password:       cfg.IMAP.Password,
		Folder:         cfg.IMAP.Folder,
		UseTLS:         cfg.IMAP.UseTLS,
		IgnoreCert:     cfg.IMAP.IgnoreCert,
		Delete:         cfg.IMAP.DeleteReported,
		RestrictDomain: cfg.IMAP.RestrictDomain,
		Interval:       time.Duration(cfg.IMAP.IntervalMinutes) * time.Minute,
	}
	if v, _ := st.Setting("imap_host"); v != "" {
		s.Host = v
	}
	if v, _ := st.Setting("imap_port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			s.Port = n
		}
	}
	if v, _ := st.Setting("imap_username"); v != "" {
		s.Username = v
	}
	if enc, _ := st.Setting("imap_password"); enc != "" {
		// Imported values are encrypted with the instance key; get the plaintext
		// via the same box the store uses.
		if plain, err := st.DecryptSetting(enc); err == nil {
			s.Password = plain
		}
	}
	if v, _ := st.Setting("imap_folder"); v != "" {
		s.Folder = v
	}
	if v, _ := st.Setting("imap_restrict_domain"); v != "" {
		s.RestrictDomain = v
	}
	for key, dst := range map[string]*bool{
		"imap_use_tls":         &s.UseTLS,
		"imap_ignore_cert":     &s.IgnoreCert,
		"imap_delete_reported": &s.Delete,
	} {
		if v, _ := st.Setting(key); v != "" {
			*dst = v == "true" || v == "1"
		}
	}
	if v, _ := st.Setting("imap_interval_minutes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.Interval = time.Duration(n) * time.Minute
		}
	}
	if v, _ := st.Setting("imap_enabled"); v == "false" {
		s.Host = ""
	}
	if s.Port == 0 {
		s.Port = 993
	}
	if s.Folder == "" {
		s.Folder = "INBOX"
	}
	if s.Interval == 0 {
		s.Interval = 5 * time.Minute
	}
	return s
}

// Monitor polls the report mailbox.
type Monitor struct {
	Settings Settings
	// Reload, when set, is called before every poll so settings changed in the
	// UI take effect without a restart.
	Reload func() Settings
	Log    *slog.Logger
	Hub    *notify.Dispatcher
}

// logger never returns nil so monitoring can start before logging is wired.
func (m *Monitor) logger() *slog.Logger {
	if m.Log == nil {
		return slog.Default()
	}
	return m.Log
}

// current returns the settings to use for this cycle.
func (m *Monitor) current() Settings {
	if m.Reload != nil {
		return m.Reload()
	}
	return m.Settings
}

// Run polls until the context is cancelled.
func (m *Monitor) Run(ctx context.Context, st *store.Store) {
	startup := m.current()
	if !startup.Enabled() {
		m.logger().Info("report mailbox monitor idle; no mailbox configured")
	} else {
		m.logger().Info("report mailbox monitor started", "host", startup.Host, "folder", startup.Folder)
	}
	// Poll once shortly after startup, then on the interval.
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			cur := m.current()
			if !cur.Enabled() {
				timer.Reset(30 * time.Second)
				continue
			}
			matched, err := m.poll(st, cur)
			if err != nil {
				m.logger().Warn("mailbox poll failed", "err", err)
			} else if matched > 0 {
				m.logger().Info("reported messages matched", "count", matched)
			}
			timer.Reset(cur.Interval)
		}
	}
}

// poll scans unseen messages once and returns how many were matched.
func (m *Monitor) poll(st *store.Store, cfg Settings) (int, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	// #nosec G402 -- InsecureSkipVerify is the operator's explicit per-mailbox choice.
	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.IgnoreCert}

	var (
		c   *imapclient.Client
		err error
	)
	if cfg.UseTLS || cfg.Port == 993 {
		c, err = imapclient.DialTLS(addr, tlsConfig)
	} else {
		c, err = imapclient.Dial(addr)
		if err == nil {
			if ok, _ := c.SupportStartTLS(); ok {
				err = c.StartTLS(tlsConfig)
			}
		}
	}
	if err != nil {
		return 0, err
	}
	defer c.Logout()

	if err := c.Login(cfg.Username, cfg.Password); err != nil {
		return 0, fmt.Errorf("login: %w", err)
	}
	mbox, err := c.Select(cfg.Folder, false)
	if err != nil {
		return 0, fmt.Errorf("select %s: %w", cfg.Folder, err)
	}
	if mbox.Messages == 0 {
		return 0, nil
	}
	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}
	ids, err := c.Search(criteria)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(ids...)

	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid, section.FetchItem()}
	messages := make(chan *imap.Message, 16)
	done := make(chan error, 1)
	go func() { done <- c.Fetch(seqset, items, messages) }()

	matched := 0
	var toDelete []uint32
	for msg := range messages {
		if msg.Envelope == nil {
			continue
		}
		if !m.allowedSender(msg.Envelope, cfg) {
			continue
		}
		rids := map[string]bool{}
		if rid := m.ridFromEnvelope(msg.Envelope, st); rid != "" {
			rids[rid] = true
		}
		if body := msg.GetBody(section); body != nil {
			for _, rid := range ScanRIDs(readLimited(body)) {
				rids[rid] = true
			}
		}
		if len(rids) == 0 {
			// Not one of ours: worth a human look, but nothing to record.
			m.logger().Info("reported message did not match a campaign",
				"from", envelopeFrom(msg.Envelope), "subject", envelopeSubject(msg.Envelope))
			continue
		}
		matchedAny := false
		for rid := range rids {
			target, campaign, err := st.TargetByRID(rid)
			if err != nil {
				continue
			}
			matchedAny = true
			if !target.ReportedAt.IsZero() {
				continue
			}
			if err := st.Track(target, store.EvEmailReported, "Reported via mailbox", "", "imap", nil, false); err != nil {
				m.logger().Warn("mark reported", "rid", rid, "err", err)
				continue
			}
			matched++
			if m.Hub != nil {
				m.Hub.Emit(campaign.OrgID, notify.Payload{
					Event: store.EvEmailReported, CampaignID: campaign.ID, Campaign: campaign.Name,
					Recipient: target.Email, RID: rid, Message: "reported via mailbox",
					At: time.Now().UTC(),
				})
			}
		}
		if matchedAny && cfg.Delete {
			toDelete = append(toDelete, msg.SeqNum)
		}
	}
	if err := <-done; err != nil {
		return matched, err
	}
	if len(toDelete) > 0 {
		delSet := new(imap.SeqSet)
		for _, seq := range toDelete {
			delSet.AddNum(seq)
		}
		if err := c.Store(delSet, imap.AddFlags, imap.DeletedFlag, nil); err == nil {
			_ = c.Expunge(nil)
		}
	}
	// Clear the unseen flag on anything left so the next poll is cheap.
	_ = c.Store(seqset, imap.AddFlags, []interface{}{imap.SeenFlag}, nil)
	return matched, nil
}

// ridFromEnvelope extracts our recipient id from Message-ID and reference
// headers.
func (m *Monitor) ridFromEnvelope(env *imap.Envelope, st *store.Store) string {
	candidates := []string{env.MessageId, env.InReplyTo}
	if env.Subject != "" {
		candidates = append(candidates, env.Subject)
	}
	for _, raw := range candidates {
		for _, token := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == '<' || r == '>' || r == '@' || r == '.' || r == ' ' || r == ',' || r == '"'
		}) {
			if len(token) == 12 && isRIDToken(token) {
				if _, _, err := st.TargetByRID(token); err == nil {
					return token
				}
			}
		}
	}
	return ""
}

func isRIDToken(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// allowedSender enforces the optional company-domain restriction.
func (m *Monitor) allowedSender(env *imap.Envelope, cfg Settings) bool {
	want := strings.ToLower(strings.TrimSpace(cfg.RestrictDomain))
	if want == "" || env == nil || len(env.From) == 0 {
		return true
	}
	from := strings.ToLower(env.From[0].Address())
	if i := strings.LastIndex(from, "@"); i >= 0 {
		return strings.TrimSuffix(from[i+1:], ".") == strings.TrimSuffix(want, ".")
	}
	return false
}

func envelopeFrom(env *imap.Envelope) string {
	if env == nil || len(env.From) == 0 {
		return ""
	}
	return env.From[0].Address()
}

func envelopeSubject(env *imap.Envelope) string {
	if env == nil {
		return ""
	}
	return env.Subject
}

// readLimited reads a message body without letting one huge email stall the
// poller.
func readLimited(rd io.Reader) []byte {
	raw, err := io.ReadAll(io.LimitReader(rd, maxScanBytes))
	if err != nil {
		return nil
	}
	return raw
}
