package gophish

import (
	"net/url"
	"testing"
)

func TestPasswordExtraction(t *testing.T) {
	cases := map[string]string{
		"mysql://user:secret@db:3306/gophish": "secret",
		"mysql://user@db:3306/gophish":        "",
		"mysql://user:p%40ss@db/gophish":      "p@ss",
	}
	for raw, want := range cases {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := password(u); got != want {
			t.Errorf("password(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestStatusMappers(t *testing.T) {
	campaigns := map[string]string{
		"Completed":   "completed",
		"Emails Sent": "completed",
		"In progress": "sending",
		"Scheduled":   "scheduled",
		"Stopped":     "stopped",
		"Error":       "error",
		"Created":     "draft",
		"":            "draft",
	}
	for in, want := range campaigns {
		if got := mapCampaignStatus(in); got != want {
			t.Errorf("mapCampaignStatus(%q) = %q, want %q", in, got, want)
		}
	}
	results := map[string]string{
		"Email Sent":     "sent",
		"Email Opened":   "opened",
		"Clicked Link":   "clicked",
		"Submitted Data": "submitted",
		"Email Reported": "reported",
		"Error":          "failed",
		"Weird":          "sent",
	}
	for in, want := range results {
		if got := mapResultStatus(in); got != want {
			t.Errorf("mapResultStatus(%q) = %q, want %q", in, got, want)
		}
	}
	events := map[string]string{
		"Email Sent":          "email.sent",
		"Error Sending Email": "email.failed",
		"Email Opened":        "email.opened",
		"Clicked Link":        "link.clicked",
		"Submitted Data":      "data.submitted",
		"Email Reported":      "email.reported",
		"Campaign Completed":  "campaign.completed",
		"Campaign Stopped":    "campaign.stopped",
		"Something Else":      "campaign.started",
	}
	for in, want := range events {
		if got := mapEventType(in); got != want {
			t.Errorf("mapEventType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostSplitting(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port int
	}{
		{"smtp.example.com:587", "smtp.example.com", 587},
		{"smtp.example.com", "smtp.example.com", 587},
		{"", "", 587},
		{"[::1]:2525", "[::1]", 2525},
	}
	for _, c := range cases {
		host, port := splitHost(c.in)
		if host != c.host || port != c.port {
			t.Errorf("splitHost(%q) = %q, %d", c.in, host, port)
		}
	}
}
