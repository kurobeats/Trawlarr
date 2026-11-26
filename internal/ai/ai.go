// Package ai drafts campaign content with an OpenAI-compatible endpoint.
//
// It is entirely optional: when disabled, no outbound request is ever made and
// the UI hides the drafting controls. Any server that implements
// POST /chat/completions works (OpenAI, Ollama, vLLM, LM Studio, LiteLLM...).
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kurobeats/trawlarr/internal/config"
)

// MaxPromptLength bounds what we forward to the model.
const MaxPromptLength = 4000

// Client talks to an OpenAI-compatible chat completions endpoint.
type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// New returns a client, or nil when the feature is disabled or incomplete.
func New(cfg config.AI) *Client {
	if !cfg.Enabled || cfg.BaseURL == "" || cfg.Model == "" {
		return nil
	}
	key := cfg.APIKey
	if key == "" {
		key = os.Getenv("TRAWLARR_AI_API_KEY")
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	return &Client{
		baseURL: strings.TrimSuffix(cfg.BaseURL, "/"),
		apiKey:  key,
		model:   cfg.Model,
		http:    &http.Client{Timeout: timeout},
	}
}

// Model returns the configured model name, for display in the UI.
func (c *Client) Model() string { return c.model }

// Request is the operator's brief.
type Request struct {
	Prompt   string `json:"prompt"`
	Tone     string `json:"tone"`
	Audience string `json:"audience"`
}

// EmailDraft is generated email content.
type EmailDraft struct {
	Subject  string `json:"subject"`
	TextBody string `json:"text_body"`
	HTMLBody string `json:"html_body"`
}

// PageDraft is generated landing page content.
type PageDraft struct {
	Name string `json:"name"`
	HTML string `json:"html"`
}

const emailSystem = `You write phishing simulation emails for an authorised corporate security-awareness programme.
Return ONE JSON object and nothing else, with exactly these string keys:
  "subject": a short subject line,
  "text_body": a plain-text body,
  "html_body": an HTML body.
Rules:
- Use the placeholders {{.FirstName}}, {{.LastName}}, {{.Position}} where a name helps.
- Put the link target as {{.URL}} and include {{.Tracker}} once, inside the HTML body.
- Do not invent attachments, logos or external image URLs.
- Keep it plausible, calm and professional. No threats, no shocking claims, no impersonation of a real brand.
- The HTML must be a fragment (no <html> or <body> tags).`

const pageSystem = `You build landing pages for an authorised phishing simulation.
Return ONE JSON object and nothing else, with exactly these string keys:
  "name": a short name for the page,
  "html": a complete, self-contained HTML document.
Rules:
- Include a form with <input name="username"> and <input name="password" type="password"> and a submit button.
- Inline all CSS in a <style> block. No external assets, no JavaScript.
- Neutral corporate styling. Do not copy a real company's branding or logo.
- The page may use the placeholder {{.FirstName}}.`

// DraftEmail asks the model for an email template.
func (c *Client) DraftEmail(ctx context.Context, req Request) (*EmailDraft, error) {
	out, err := c.completeJSON(ctx, emailSystem, brief(req))
	if err != nil {
		return nil, err
	}
	var draft EmailDraft
	if err := json.Unmarshal(out, &draft); err != nil {
		return nil, fmt.Errorf("model returned unusable JSON: %w", err)
	}
	if strings.TrimSpace(draft.Subject) == "" && strings.TrimSpace(draft.HTMLBody) == "" {
		return nil, errors.New("model returned empty content")
	}
	if draft.HTMLBody == "" {
		draft.HTMLBody = "<p>" + htmlEscape(draft.TextBody) + "</p>"
	}
	return &draft, nil
}

// DraftPage asks the model for a landing page.
func (c *Client) DraftPage(ctx context.Context, req Request) (*PageDraft, error) {
	out, err := c.completeJSON(ctx, pageSystem, brief(req))
	if err != nil {
		return nil, err
	}
	var draft PageDraft
	if err := json.Unmarshal(out, &draft); err != nil {
		return nil, fmt.Errorf("model returned unusable JSON: %w", err)
	}
	if !strings.Contains(draft.HTML, "<form") {
		return nil, errors.New("generated page has no form; try a more specific prompt")
	}
	if draft.Name == "" {
		draft.Name = "Generated page"
	}
	return &draft, nil
}

func brief(req Request) string {
	var b strings.Builder
	b.WriteString("Brief: ")
	b.WriteString(strings.TrimSpace(req.Prompt))
	if req.Tone != "" {
		b.WriteString("\nTone: ")
		b.WriteString(req.Tone)
	}
	if req.Audience != "" {
		b.WriteString("\nAudience: ")
		b.WriteString(req.Audience)
	}
	return b.String()
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return strings.ReplaceAll(r.Replace(s), "\n", "<br>")
}

// Summary is the aggregate campaign data sent for analysis. It deliberately
// contains no recipient addresses or captured values.
type Summary struct {
	Name       string
	Status     string
	Recipients int
	Sent       int
	Opened     int
	Clicked    int
	Submitted  int
	Reported   int
	Failed     int
	Uncertain  int
	SendRate   int
	Duration   string
}

const analysisSystem = `You analyse the results of an authorised phishing simulation for a security team.
Write a short, factual assessment in plain text (no markdown, no tables), at most 200 words, covering:
1. what the outcome rates suggest about risk,
2. the single most useful next action (for example which cohort to re-train),
3. one caveat about what these numbers cannot tell you.
Never invent numbers that were not given. Never address the recipients personally.`

// Analyze returns a plain-text assessment of a campaign's results.
func (c *Client) Analyze(ctx context.Context, sum Summary) (string, error) {
	user := fmt.Sprintf(`Campaign: %s
Status: %s
Duration: %s
Recipients: %d, emails sent: %d, delivery failures: %d, delivery uncertain: %d
Opened: %d, clicked: %d, submitted data: %d, reported as phishing: %d
Configured send rate: %d emails/hour`,
		sum.Name, sum.Status, sum.Duration,
		sum.Recipients, sum.Sent, sum.Failed, sum.Uncertain,
		sum.Opened, sum.Clicked, sum.Submitted, sum.Reported, sum.SendRate)
	text, err := c.complete(ctx, analysisSystem, user)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("ai endpoint returned an empty analysis")
	}
	return text, nil
}

// complete returns the raw assistant message.
func (c *Client) complete(ctx context.Context, system, user string) (string, error) {
	if len(user) > MaxPromptLength {
		return "", fmt.Errorf("prompt is longer than %d characters", MaxPromptLength)
	}
	body := map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0.7,
		"max_tokens":  2000,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	url := c.baseURL
	if !strings.HasSuffix(url, "/chat/completions") {
		url += "/chat/completions"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ai endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ai endpoint returned %s: %s", resp.Status, firstLines(string(raw), 400))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("ai endpoint returned unexpected JSON: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", errors.New("ai endpoint error: " + parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("ai endpoint returned no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

// completeJSON is complete plus tolerant JSON extraction.
func (c *Client) completeJSON(ctx context.Context, system, user string) ([]byte, error) {
	content, err := c.complete(ctx, system, user)
	if err != nil {
		return nil, err
	}
	return extractJSON(content), nil
}

// extractJSON tolerates models that wrap the object in prose or code fences.
func extractJSON(content string) []byte {
	s := strings.TrimSpace(content)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		s = strings.TrimPrefix(s, "json")
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		s = s[start : end+1]
	}
	return []byte(strings.TrimSpace(s))
}

func firstLines(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
