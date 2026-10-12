// Command f95-downloader is the M6 download worker daemon.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/jeiang/f95-tracker/internal/worker"
	"github.com/jeiang/f95-tracker/internal/worker/api"
)

func main() {
	cfg, err := worker.LoadConfig(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "f95-downloader:", err)
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "f95-downloader:", err)
		os.Exit(2)
	}
	for _, tool := range []string{"mega-get", "gallery-dl", "7zz", "unar"} {
		if _, err := exec.LookPath(tool); err != nil {
			log.Warn("tool not found on PATH", "tool", tool)
		}
	}
	w, err := worker.New(worker.Options{
		Client:           &api.Client{BaseURL: cfg.TrackerURL, Token: cfg.Token, HTTP: &http.Client{Timeout: 30 * time.Second}},
		GamesDir:         cfg.GamesDir,
		PollInterval:     cfg.PollInterval,
		ProgressInterval: 5 * time.Second,
		Hosts:            worker.DefaultHosts(cfg.StateDir, "https://pixeldrain.com"),
		Log:              log,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "f95-downloader:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	log.Info("started", "tracker", cfg.TrackerURL, "games_dir", cfg.GamesDir)
	w.Run(ctx)
}
