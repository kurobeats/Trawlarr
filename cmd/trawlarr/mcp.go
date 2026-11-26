package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kurobeats/trawlarr/internal/ai"
	"github.com/kurobeats/trawlarr/internal/config"
	"github.com/kurobeats/trawlarr/internal/db"
	"github.com/kurobeats/trawlarr/internal/mcp"
	"github.com/kurobeats/trawlarr/internal/secret"
	"github.com/kurobeats/trawlarr/internal/store"
)

// runMCP implements `trawlarr mcp`, a stdio Model Context Protocol server.
func runMCP(args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	configPath := fs.String("config", "config.json", "path to the config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	if !cfg.MCP.Enabled {
		fmt.Fprintln(os.Stderr, "MCP is disabled. Set \"mcp\": {\"enabled\": true} in", *configPath)
		return 1
	}
	key, err := base64Decode(cfg.EncryptionKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "encryption key:", err)
		return 1
	}
	box, err := secret.New(key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "encryption key:", err)
		return 1
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "database:", err)
		return 1
	}
	defer conn.Close()
	log := newLogger(cfg.LogLevel)
	mcp.SetVersion(version)

	srv, err := mcp.New(mcp.Deps{
		Store: store.New(conn, box),
		AI:    ai.New(cfg.AI),
		Cfg:   cfg,
		Log:   log,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		return 1
	}
	return 0
}
