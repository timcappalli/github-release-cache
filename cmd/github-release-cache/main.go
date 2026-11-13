package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/timcappalli/github-release-cache/internal/config"
	"github.com/timcappalli/github-release-cache/internal/github"
	"github.com/timcappalli/github-release-cache/internal/server"
	"github.com/timcappalli/github-release-cache/internal/syncer"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "run HTTP healthcheck against /healthz and exit")
	flag.Parse()

	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.LogLevel)

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		logger.Error("failed to create data directory", "dir", cfg.DataDir, "error", err)
		os.Exit(1)
	}

	ghClient := github.NewClient(cfg.GitHubAPIURL)
	syncers := make([]*syncer.Syncer, 0, len(cfg.Repos))
	for _, repoCfg := range cfg.Repos {
		syncers = append(syncers, syncer.New(repoCfg, cfg.DataDir, ghClient, logger))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := server.NewServer(cfg.ListenAddr, cfg.DataDir)
	srvErr := make(chan error, 1)
	go func() {
		logger.Info("starting HTTP server", "addr", cfg.ListenAddr, "data_dir", cfg.DataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr <- err
		}
		close(srvErr)
	}()

	runSyncAll(ctx, syncers, logger)

	for {
		next := cfg.NextCheck(time.Now())
		wait := time.Until(next)
		logger.Debug("scheduled next check", "at", next.Format(time.RFC3339), "in", wait.Round(time.Second))
		timer := time.NewTimer(wait)

		select {
		case <-ctx.Done():
			timer.Stop()
			logger.Info("shutting down")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				logger.Error("HTTP server shutdown error", "error", err)
			}
			return
		case err, ok := <-srvErr:
			timer.Stop()
			if ok && err != nil {
				logger.Error("HTTP server failed", "error", err)
				os.Exit(1)
			}
		case <-timer.C:
			runSyncAll(ctx, syncers, logger)
		}
	}
}

func runSyncAll(ctx context.Context, syncers []*syncer.Syncer, logger *slog.Logger) {
	for _, s := range syncers {
		if ctx.Err() != nil {
			return
		}
		if err := s.Sync(ctx); err != nil {
			logger.Error("sync failed", "error", err)
		}
	}
}

func runHealthcheck() int {
	addr := strings.TrimSpace(os.Getenv("LISTEN_ADDR"))
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func newLogger(levelStr string) *slog.Logger {
	var level slog.Level
	switch levelStr {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
