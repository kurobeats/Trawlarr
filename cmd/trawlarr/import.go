package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/kurobeats/trawlarr/internal/config"
	"github.com/kurobeats/trawlarr/internal/db"
	"github.com/kurobeats/trawlarr/internal/gophish"
	"github.com/kurobeats/trawlarr/internal/secret"
	"github.com/kurobeats/trawlarr/internal/store"
)

func base64Decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// runImport implements `trawlarr import-gophish`.
func runImport(args []string) int {
	fs := flag.NewFlagSet("import-gophish", flag.ExitOnError)
	configPath := fs.String("config", "config.json", "path to the Trawlarr config file")
	from := fs.String("from", "", "gophish database: a SQLite file path or mysql://user:pass@host:3306/gophish")
	force := fs.Bool("force", false, "import even if the destination already has data")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
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
	st := store.New(conn, box)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	rep, err := gophish.Import(context.Background(), gophish.Options{Source: *from, Dest: st, Log: log, Force: *force})
	if err != nil {
		fmt.Fprintln(os.Stderr, "import failed:", err)
		return 1
	}
	fmt.Printf("Imported into %s\n", cfg.DBPath)
	fmt.Printf("  users %d, groups %d, targets %d, templates %d, attachments %d,\n", rep.Users, rep.Groups, rep.Targets, rep.Templates, rep.Attachments)
	fmt.Printf("  pages %d, profiles %d, campaigns %d, results %d, events %d, webhooks %d\n", rep.Pages, rep.Profiles, rep.Campaigns, rep.Results, rep.Events, rep.Webhooks)
	for _, w := range rep.Warnings {
		fmt.Println("  ! " + w)
	}
	return 0
}
