package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kurobeats/trawlarr/internal/ai"
	"github.com/kurobeats/trawlarr/internal/store"
)

// handleAIStatus tells the UI whether to show the drafting controls.
// handleAnalyzeCampaign asks the AI endpoint for a risk assessment of one
// campaign. Only aggregate numbers are sent, never recipient data.
func (s *Server) handleAnalyzeCampaign(w http.ResponseWriter, r *http.Request) {
	if s.AI == nil {
		writeError(w, http.StatusServiceUnavailable,
			"AI analysis is disabled; set ai.enabled, ai.base_url and ai.model in config.json")
		return
	}
	me := UserFrom(r)
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.limiter.allow("ai:"+strconv.FormatInt(me.ID, 10), 30, 15*time.Minute) {
		writeError(w, http.StatusTooManyRequests, "too many AI requests, try again later")
		return
	}
	c, err := s.Store.Campaign(me.OrgID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	text, err := s.AI.Analyze(ctx, ai.Summary{
		Name: c.Name, Status: c.Status,
		Recipients: c.Stats.Total, Sent: c.Stats.Sent,
		Opened: c.Stats.Opened, Clicked: c.Stats.Clicked,
		Submitted: c.Stats.Submitted, Reported: c.Stats.Reported,
		Failed: c.Stats.Failed, Uncertain: c.Stats.Uncertain,
		SendRate: c.SendRate,
		Duration: campaignDuration(c),
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Store.Audit(me.OrgID, me.ID, "campaign.analyzed", c.Name, "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]string{"analysis": text})
}

// campaignDuration describes how long a campaign took.
func campaignDuration(c *store.Campaign) string {
	if c.StartedAt.IsZero() || c.CompletedAt.IsZero() {
		return "unknown"
	}
	d := c.CompletedAt.Time().Sub(c.StartedAt.Time())
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}

func (s *Server) handleAIStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"enabled": s.AI != nil}
	if s.AI != nil {
		out["model"] = s.AI.Model()
	}
	writeJSON(w, http.StatusOK, out)
}

type aiRequest struct {
	Kind     string `json:"kind"`
	Prompt   string `json:"prompt"`
	Tone     string `json:"tone"`
	Audience string `json:"audience"`
}

// handleAIGenerate drafts an email template or a landing page.
func (s *Server) handleAIGenerate(w http.ResponseWriter, r *http.Request) {
	if s.AI == nil {
		writeError(w, http.StatusServiceUnavailable,
			"AI drafting is disabled; set ai.enabled, ai.base_url and ai.model in config.json")
		return
	}
	me := UserFrom(r)
	if !s.limiter.allow("ai:"+strconv.FormatInt(me.ID, 10), 30, 15*time.Minute) {
		writeError(w, http.StatusTooManyRequests, "too many drafting requests, try again later")
		return
	}
	var req aiRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	if len(req.Prompt) > ai.MaxPromptLength {
		writeError(w, http.StatusBadRequest, "prompt is too long")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	brief := ai.Request{Prompt: req.Prompt, Tone: req.Tone, Audience: req.Audience}

	s.Store.Audit(me.OrgID, me.ID, "ai.draft", req.Kind, "", s.clientIP(r))
	switch req.Kind {
	case "email", "":
		draft, err := s.AI.DraftEmail(ctx, brief)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, draft)
	case "landing_page":
		draft, err := s.AI.DraftPage(ctx, brief)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, draft)
	default:
		writeError(w, http.StatusBadRequest, "kind must be email or landing_page")
	}
}
