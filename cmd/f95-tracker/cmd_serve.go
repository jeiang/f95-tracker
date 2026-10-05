package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jeiang/f95-tracker/internal/auth"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/config"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/itch"
	"github.com/jeiang/f95-tracker/internal/notify"
	"github.com/jeiang/f95-tracker/internal/web"
)

func runServe(ctx context.Context, args []string) error {
	cfg, pos, err := config.Load("serve", args, os.Getenv)
	if err != nil {
		return errUsage(err.Error())
	}
	if len(pos) > 0 {
		return errUsage("serve takes no arguments")
	}
	base := newLogger(cfg.LogLevel)
	log := base.With("component", "serve")

	store, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()

	clk := clock.Real{}
	deps := web.Deps{
		Store:  store,
		Clock:  clk,
		Log:    base,
		Config: cfg,
		Auth:   auth.New(cfg, store, clk, base),
		Games:  games.New(store, clk, games.Options{StateDir: cfg.StateDir}),
		Notify: notify.New(store, clk, notify.Options{URL: cfg.NtfyURL, Topic: cfg.NtfyTopic, Token: cfg.NtfyToken, BaseURL: cfg.BaseURL}),
		Itch:   itch.NewClient(itch.Options{Clock: clk, Version: version}),
	}
	srv := &http.Server{
		Handler:           web.New(deps).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	log.Info("listening", "addr", ln.Addr().String(), "version", version)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
