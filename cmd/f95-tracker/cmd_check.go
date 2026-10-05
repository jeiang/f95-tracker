package main

import (
	"context"
	"flag"
	"os"

	"github.com/jeiang/f95-tracker/internal/check"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/config"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/itch"
	"github.com/jeiang/f95-tracker/internal/notify"
	"github.com/jeiang/f95-tracker/internal/tags"
)

func runCheck(ctx context.Context, args []string) error {
	var opts check.Options
	cfg, pos, err := config.Load("check", args, os.Getenv, func(fs *flag.FlagSet) {
		fs.BoolVar(&opts.NoDigest, "no-digest", false, "run the checks but send no digest (development)")
		fs.BoolVar(&opts.OnlyChecker, "only-checker", false, "run only the checker.php step (and the digest)")
	})
	if err != nil {
		return errUsage(err.Error())
	}
	if len(pos) > 0 {
		return errUsage("check takes no arguments")
	}
	log := newLogger(cfg.LogLevel)

	store, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()

	clk := clock.Real{}
	creds := f95.NewCredStore(store, clk)
	f95c, err := f95.New(f95.Options{
		BaseURL: cfg.F95BaseURL,
		Pacer:   f95.NewPacer(cfg.F95LockPath()),
		Creds:   creds,
		Retry:   f95.DefaultRetry,
		Version: version,
	})
	if err != nil {
		return err
	}
	defer f95c.Close()
	gs := games.New(store, clk, games.Options{StateDir: cfg.StateDir})
	ts := tags.New(store, clk)
	ic := itch.NewClient(itch.Options{Clock: clk, Version: version})

	return check.NewRunner(check.Deps{
		Store:     store,
		Clock:     clk,
		Tags:      ts,
		Notify:    notify.New(store, clk, notify.Options{URL: cfg.NtfyURL, Topic: cfg.NtfyTopic, Token: cfg.NtfyToken, BaseURL: cfg.BaseURL}),
		F95:       f95c,
		Creds:     creds,
		Itch:      ic,
		Refresher: check.NewRefresher(store, clk, gs, ts, f95c, ic, log),
		Log:       log,
		LockPath:  cfg.CheckLockPath(),
	}).Run(ctx, opts)
}
