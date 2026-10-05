package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jeiang/f95-tracker/internal/clock"
	"github.com/jeiang/f95-tracker/internal/config"
	"github.com/jeiang/f95-tracker/internal/db"
	"github.com/jeiang/f95-tracker/internal/f95"
	"github.com/jeiang/f95-tracker/internal/fixture"
)

var threadIDRe = regexp.MustCompile(`^\d+$`)

// runFixture records one scrubbed thread page: f95-tracker fixture <thread-id> [--out FILE].
func runFixture(ctx context.Context, args []string) error {
	var out string
	cfg, pos, err := config.Load("fixture", args, os.Getenv, func(fs *flag.FlagSet) {
		fs.StringVar(&out, "out", "", "output file (default testdata/f95/<id>.html)")
	})
	if err != nil {
		return errUsage(err.Error())
	}
	if len(pos) != 1 || !threadIDRe.MatchString(pos[0]) {
		return errUsage("usage: f95-tracker fixture <thread-id> [--out testdata/f95/<id>.html] [--state-dir DIR]")
	}
	id := pos[0]
	if out == "" {
		out = filepath.Join("testdata", "f95", id+".html")
	}

	store, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	creds := f95.NewCredStore(store, clock.Real{})
	pacer := f95.NewPacer(cfg.F95LockPath())
	client, err := f95.New(f95.Options{
		BaseURL: cfg.F95BaseURL, Pacer: pacer, Creds: creds, Version: version, Interactive: true,
	})
	if err != nil {
		return err
	}
	defer client.Close()

	page, err := fixture.Record(ctx, client, creds, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, page, 0o644); err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}
