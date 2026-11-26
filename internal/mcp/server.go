// Package mcp exposes Trawlarr to Model Context Protocol clients over stdio.
//
// The server is deliberately stdio-only: it runs inside a process the operator
// already controls, so there is no unauthenticated network listener to secure.
// Tool calls act as one configured account and are written to the audit log.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	mcp_golang "github.com/metoro-io/mcp-golang"
	"github.com/metoro-io/mcp-golang/transport"
	"github.com/metoro-io/mcp-golang/transport/stdio"

	"github.com/kurobeats/trawlarr/internal/ai"
	"github.com/kurobeats/trawlarr/internal/config"
	"github.com/kurobeats/trawlarr/internal/store"
)

// Deps is everything the MCP tools need.
type Deps struct {
	Store  *store.Store
	AI     *ai.Client
	Cfg    *config.Config
	Log    *slog.Logger
	// Actor is the account tool calls act as. Defaults to the first admin.
	Actor *store.User
}

// Server wraps the MCP protocol server.
type Server struct {
	store  *store.Store
	ai     *ai.Client
	cfg    *config.Config
	log    *slog.Logger
	actor  *store.User
	mcp    *mcp_golang.Server
}

// New builds a server that reads and writes on stdin/stdout.
func New(d Deps) (*Server, error) {
	return newServer(d, stdio.NewStdioServerTransport())
}

// NewWithIO builds a server on custom streams, used by tests.
func NewWithIO(d Deps, in io.Reader, out io.Writer) (*Server, error) {
	return newServer(d, stdio.NewStdioServerTransportWithIO(in, out))
}

func newServer(d Deps, tr transport.Transport) (*Server, error) {
	if d.Store == nil || d.Cfg == nil {
		return nil, errors.New("mcp: store and config are required")
	}
	if !d.Cfg.MCP.Enabled {
		return nil, errors.New("mcp: disabled in config (set mcp.enabled to true)")
	}
	actor := d.Actor
	if actor == nil && d.Cfg.MCP.User != "" {
		u, err := d.Store.UserByUsername(d.Cfg.MCP.User)
		if err != nil {
			return nil, fmt.Errorf("mcp: user %q not found", d.Cfg.MCP.User)
		}
		actor = u
	}
	if actor == nil {
		var err error
		if actor, err = d.Store.FirstAdmin(); err != nil {
			return nil, fmt.Errorf("mcp: no admin account to act as: %w", err)
		}
	}
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	s := &Server{store: d.Store, ai: d.AI, cfg: d.Cfg, log: log, actor: actor}
	s.mcp = mcp_golang.NewServer(tr,
		mcp_golang.WithName("trawlarr"),
		mcp_golang.WithVersion(version),
		mcp_golang.WithInstructions(instructions),
	)
	if err := s.registerTools(); err != nil {
		return nil, err
	}
	return s, nil
}

// Actor returns the account tools act as.
func (s *Server) Actor() *store.User { return s.actor }

// Serve starts the protocol loop and blocks until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	if err := s.mcp.Serve(); err != nil {
		return err
	}
	s.log.Info("mcp server ready", "actor", s.actor.Username, "writes", s.cfg.MCP.AllowWrites)
	<-ctx.Done()
	return nil
}

// SetVersion records the build version in the MCP handshake.
func SetVersion(v string) {
	if v != "" {
		version = v
	}
}

var version = "dev"

const instructions = `Trawlarr is a phishing simulation platform for authorised security-awareness work.
Tools return JSON. Read tools work immediately; create/launch/stop tools require
mcp.allow_writes in the server configuration. Recipient addresses are personal
data: only use them for the campaign you were asked about.`

func (s *Server) registerTools() error {
	tools := []struct {
		name, description string
		handler           any
	}{
		// read-only
		{"dashboard", "Aggregate campaign stats for the organisation.", s.dashboard},
		{"list_campaigns", "List campaigns with their funnel stats.", s.listCampaigns},
		{"get_campaign", "Campaign detail: stats, recipients and recent timeline.", s.getCampaign},
		{"export_campaign", "Campaign results as CSV text.", s.exportCampaign},
		{"analyze_campaign", "Ask the configured AI endpoint for a short risk assessment of a campaign.", s.analyzeCampaign},
		{"list_groups", "List recipient groups.", s.listGroups},
		{"list_templates", "List email templates.", s.listTemplates},
		{"get_template", "Full content of one email template.", s.getTemplate},
		{"list_landing_pages", "List landing pages.", s.listPages},
		{"list_sending_profiles", "List sending profiles (no credentials).", s.listProfiles},
		{"draft_email", "Draft an email template with the configured AI endpoint.", s.draftEmail},
		{"draft_landing_page", "Draft a landing page with the configured AI endpoint.", s.draftLandingPage},

		// writes
		{"create_group", "Create a recipient group from CSV text.", s.createGroup},
		{"create_template", "Create an email template.", s.createTemplate},
		{"create_landing_page", "Create a landing page.", s.createLandingPage},
		{"create_campaign", "Create a campaign draft.", s.createCampaign},
		{"launch_campaign", "Launch a campaign now.", s.launchCampaign},
		{"stop_campaign", "Stop a running campaign.", s.stopCampaign},
	}
	for _, t := range tools {
		if err := s.mcp.RegisterTool(t.name, t.description, t.handler); err != nil {
			return fmt.Errorf("mcp: register %s: %w", t.name, err)
		}
	}
	return nil
}

// requireWrite blocks mutating tools unless the operator opted in.
func (s *Server) requireWrite(tool string) error {
	if !s.cfg.MCP.AllowWrites {
		return fmt.Errorf("%s is a write tool and mcp.allow_writes is false", tool)
	}
	if store.Rank(s.actor.Role) < store.Rank(store.RoleOperator) {
		return fmt.Errorf("%s needs an operator or admin account; this server acts as %s", tool, s.actor.Role)
	}
	return nil
}

func (s *Server) audit(tool, target, detail string) {
	s.store.Audit(s.actor.OrgID, s.actor.ID, "mcp."+tool, target, detail, "stdio")
}

// jsonResponse renders a tool result as pretty JSON text. HTML escaping is off
// so template and page markup stays readable for the assistant.
func jsonResponse(v any) (*mcp_golang.ToolResponse, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return mcp_golang.NewToolResponse(mcp_golang.NewTextContent(buf.String())), nil
}
