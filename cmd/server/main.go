package main

import (
	"context"
	"errors"
	"github.com/koboshchan/anyshell/internal/config"
	"github.com/koboshchan/anyshell/internal/server"
	"github.com/koboshchan/anyshell/internal/store"
	"github.com/koboshchan/anyshell/internal/version"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)
	dev := os.Getenv("ANYSHELL_DEV") == "1"
	db, err := store.Open(config.Env("ANYSHELL_DB", "data/anyshell.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	cfg := server.Config{PublicURL: config.Env("ANYSHELL_PUBLIC_URL", "https://localhost"), Dev: dev, ReleaseVersion: os.Getenv("ANYSHELL_RELEASE_VERSION"), ReleaseURL: os.Getenv("ANYSHELL_RELEASE_URL"), ReleaseDir: os.Getenv("ANYSHELL_RELEASE_DIR")}
	for _, cidr := range strings.Split(os.Getenv("ANYSHELL_TRUSTED_PROXIES"), ",") {
		if strings.TrimSpace(cidr) == "" {
			continue
		}
		p, e := netip.ParsePrefix(strings.TrimSpace(cidr))
		if e != nil {
			return e
		}
		cfg.TrustedProxies = append(cfg.TrustedProxies, p)
	}
	app, err := server.New(db, cfg, log)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: config.Env("ANYSHELL_LISTEN", "127.0.0.1:8080"), Handler: app, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelError)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	log.Info("server listening", "address", srv.Addr, "version", version.Version, "development", dev)
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
