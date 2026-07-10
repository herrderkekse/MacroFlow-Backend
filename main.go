// Command macroflow-sync is a minimal, self-hosted change-log relay that
// synchronises MacroFlow's local-first SQLite data across a single user's
// devices. See SYNC.md for the protocol.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/herrderkekse/MacroFlow-Backend/internal/api"
	"github.com/herrderkekse/MacroFlow-Backend/internal/config"
	"github.com/herrderkekse/MacroFlow-Backend/internal/store"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz endpoint and exit (for container healthchecks)")
	flag.Parse()

	if *healthcheck {
		runHealthcheck()
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration error", "err", err)
		os.Exit(1)
	}

	if dir := filepath.Dir(cfg.DBPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Error("cannot create database directory", "dir", dir, "err", err)
			os.Exit(1)
		}
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Error("cannot open database", "path", cfg.DBPath, "err", err)
		os.Exit(1)
	}
	defer st.Close()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(cfg, st, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: large push payloads (base64 photos) can take a while
		// to read; MaxBytesReader bounds memory instead.
		IdleTimeout: 60 * time.Second,
	}

	// Run the server and wait for a termination signal.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listening", "addr", cfg.Addr, "db", cfg.DBPath, "users", len(cfg.Users))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
	}
}

// runHealthcheck probes the local /healthz endpoint and exits non-zero on
// failure. Used as the container HEALTHCHECK since the distroless image has no
// shell or curl. It honours PORT so it targets the same address the server uses.
func runHealthcheck() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}
