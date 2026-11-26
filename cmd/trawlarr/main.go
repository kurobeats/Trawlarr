// Command trawlarr runs the phishing simulation server.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kurobeats/trawlarr/internal/api"
	"github.com/kurobeats/trawlarr/internal/auth"
	"github.com/kurobeats/trawlarr/internal/config"
	"github.com/kurobeats/trawlarr/internal/db"
	"github.com/kurobeats/trawlarr/internal/engine"
	"github.com/kurobeats/trawlarr/internal/imapmon"
	"github.com/kurobeats/trawlarr/internal/mail"
	"github.com/kurobeats/trawlarr/internal/notify"
	"github.com/kurobeats/trawlarr/internal/secret"
	"github.com/kurobeats/trawlarr/internal/store"
	"github.com/kurobeats/trawlarr/internal/web"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "import-gophish":
			os.Exit(runImport(os.Args[2:]))
		case "mcp":
			os.Exit(runMCP(os.Args[2:]))
		case "version", "-version", "--version":
			fmt.Println("trawlarr", version)
			return
		case "help", "-h", "--help":
			usage()
			return
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `trawlarr - phishing simulation platform

  trawlarr [-config config.json]        run the server
  trawlarr mcp [-config config.json]    run the Model Context Protocol server (stdio)
  trawlarr import-gophish -from FILE    import a gophish SQLite database
  trawlarr version                      print the version
`)
}

func run() error {
	fs := flag.NewFlagSet("trawlarr", flag.ExitOnError)
	configPath := fs.String("config", "config.json", "path to the config file")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	log.Info("starting trawlarr", "version", version, "config", *configPath)

	key, err := decodeKey(cfg.EncryptionKey)
	if err != nil {
		return err
	}
	box, err := secret.New(key)
	if err != nil {
		return err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	st := store.New(conn, box)

	if err := seedAdmin(st, cfg, log); err != nil {
		return err
	}

	api.Version = version
	hub := notify.NewDispatcher(st, log, 2)
	sender := &mail.SMTP{Log: log}
	eng := engine.New(st, sender, hub, log, func(c *store.Campaign) string {
		if c.URL != "" {
			return c.URL
		}
		if v, _ := st.Setting("phish_public_url"); v != "" {
			return v
		}
		return cfg.PhishServer.BaseURL()
	})
	ui, err := web.Dist()
	if err != nil {
		return err
	}
	srv := api.New(st, eng, sender, hub, cfg, log, ui)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv.Power = powerControl(stop, log)
	go eng.Start(ctx)
	go purgeSessions(ctx, st)
	monitor := &imapmon.Monitor{
		Settings: imapmon.Load(st, cfg),
		Reload:   func() imapmon.Settings { return imapmon.Load(st, cfg) },
		Log:      log,
		Hub:      hub,
	}
	go monitor.Run(ctx, st)

	errs := make(chan error, 2)
	servers := []*http.Server{}
	start := func(name string, s config.Server, handler http.Handler) error {
		if s.Addr() == "" {
			return nil
		}
		httpSrv := &http.Server{
			Addr:              s.Addr(),
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20,
		}
		servers = append(servers, httpSrv)
		go func() {
			log.Info("listening", "server", name, "addr", s.Addr(), "tls", s.UseTLS)
			var err error
			if s.UseTLS {
				httpSrv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
				err = httpSrv.ListenAndServeTLS(s.CertPath, s.KeyPath)
			} else {
				err = httpSrv.ListenAndServe()
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s server: %w", name, err)
			}
		}()
		return nil
	}

	if cfg.SameListener() {
		if err := start("admin+phish", cfg.AdminServer, srv.CombinedHandler()); err != nil {
			return err
		}
	} else {
		if err := start("admin", cfg.AdminServer, srv.AdminHandler()); err != nil {
			return err
		}
		if err := start("phish", cfg.PhishServer, srv.PhishHandler()); err != nil {
			return err
		}
	}

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errs:
		stop()
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, httpSrv := range servers {
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("shutdown", "err", err)
		}
	}
	return nil
}

// powerControl wires the sidebar power menu to this process. Restart re-execs
// the same binary with the same arguments, so it works under Docker (PID 1),
// systemd and a plain shell alike.
func powerControl(stop context.CancelFunc, log *slog.Logger) *api.PowerControl {
	return &api.PowerControl{
		Shutdown: func() { stop() },
		Restart: func() error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			log.Info("re-executing the server", "path", exe)
			// syscall.Exec only returns on failure.
			return syscall.Exec(exe, os.Args, os.Environ())
		},
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}

func decodeKey(b64 string) ([]byte, error) {
	return base64Decode(b64)
}

// seedAdmin creates the first account and prints a one-time password when the
// operator did not supply one.
func seedAdmin(st *store.Store, cfg *config.Config, log *slog.Logger) error {
	password := cfg.SeedAdminPassword
	generated := false
	if password == "" {
		password = auth.NewToken(18)
		generated = true
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	created, err := st.Seed("admin", hash)
	if err != nil {
		return err
	}
	if created && generated {
		fmt.Fprintf(os.Stderr, "\n  Trawlarr created the admin account.\n  username: admin\n  password: %s\n\n  Change it after your first login.\n\n", password)
	} else if created {
		log.Info("created admin account")
	}
	return nil
}

func purgeSessions(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := st.PurgeSessions(); err != nil {
				slog.Warn("purge sessions", "err", err)
			}
		}
	}
}
