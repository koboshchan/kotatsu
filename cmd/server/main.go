package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/koboshchan/kotatsu/internal/config"
	"github.com/koboshchan/kotatsu/internal/server"
	"github.com/koboshchan/kotatsu/internal/store"
	"github.com/koboshchan/kotatsu/internal/version"
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
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		c := &http.Client{Timeout: 3 * time.Second}
		req, e := http.NewRequest("GET", "http://127.0.0.1:8080/healthz", nil)
		if e != nil {
			return e
		}
		req.Header.Set("X-Forwarded-Proto", "https")
		resp, e := c.Do(req)
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("healthcheck status %d", resp.StatusCode)
		}
		return nil
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)
	dev := os.Getenv("KOTATSU_DEV") == "1"
	db, err := store.Open(config.Env("KOTATSU_DB", "data/kotatsu.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	cfg := server.Config{PublicURL: config.Env("KOTATSU_PUBLIC_URL", "https://localhost"), Dev: dev, ReleaseVersion: os.Getenv("KOTATSU_RELEASE_VERSION"), ReleaseURL: os.Getenv("KOTATSU_RELEASE_URL"), ReleaseDir: os.Getenv("KOTATSU_RELEASE_DIR")}
	for _, cidr := range strings.Split(os.Getenv("KOTATSU_TRUSTED_PROXIES"), ",") {
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
	srv := &http.Server{Addr: config.Env("KOTATSU_LISTEN", "127.0.0.1:8080"), Handler: app, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelError)}
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
	_ = app.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
