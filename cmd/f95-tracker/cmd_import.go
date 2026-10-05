package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/jeiang/f95-tracker/internal/check"
	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/config"
	"github.com/jeiang/f95-tracker/internal/csvimport"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/games"
	"github.com/jeiang/f95-tracker/internal/itch"
	"github.com/jeiang/f95-tracker/internal/tags"
)

func runImport(ctx context.Context, args []string) error {
	var noBackfill *bool
	cfg, pos, err := config.Load("import-csv", args, os.Getenv, func(fs *flag.FlagSet) {
		noBackfill = fs.Bool("no-backfill", false, "write the Games and baseline versions but skip the F95 detail fetches")
	})
	if err != nil {
		return errUsage(err.Error())
	}
	if len(pos) != 1 {
		return errUsage("usage: f95-tracker import-csv <file> [--no-backfill] [--state-dir DIR]")
	}
	sheet, err := os.Open(pos[0])
	if err != nil {
		return err
	}
	defer sheet.Close()

	log := newLogger(cfg.LogLevel).With("component", "import-csv")
	store, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()

	clk := clock.Real{}
	f95c, err := f95.New(f95.Options{
		BaseURL: cfg.F95BaseURL,
		Pacer:   f95.NewPacer(cfg.F95LockPath()),
		Creds:   f95.NewCredStore(store, clk),
		Retry:   f95.DefaultRetry,
		Version: version,
	})
	if err != nil {
		return err
	}
	defer f95c.Close()
	gs := games.New(store, clk, games.Options{StateDir: cfg.StateDir})
	ts := tags.New(store, clk)
	im := &csvimport.Importer{
		Store: store, Clock: clk, Games: gs, Tags: ts, F95: f95c, Log: log,
		Refresher: check.NewRefresher(store, clk, gs, ts, f95c, itch.NewClient(itch.Options{Clock: clk, Version: version}), log),
	}
	sum, err := im.Run(ctx, sheet, !*noBackfill)
	fmt.Printf("rows read %d, games created %d (merged rows %d, converted to manual %d), rows skipped %d, baselined %d, backfill done %d, backfill remaining %d\n",
		sum.Rows, sum.Games, sum.Merged, sum.Converted, sum.Skipped, sum.Baselined, sum.BackfillDone, sum.BackfillRemaining)
	if errors.Is(err, csvimport.ErrPaused) {
		fmt.Fprintln(os.Stderr, "import paused; fix the cause and re-run to resume")
	}
	return err
}
