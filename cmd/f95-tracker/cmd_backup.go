package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jeiang/f95-tracker/internal/backup"
	"github.com/jeiang/f95-tracker/internal/config"
	"github.com/jeiang/f95-tracker/internal/db"
)

func runBackup(ctx context.Context, args []string) error {
	cfg, pos, err := config.Load("backup", args, os.Getenv)
	if err != nil {
		return errUsage(err.Error())
	}
	if len(pos) != 1 {
		return errUsage("usage: f95-tracker backup <dest> [--state-dir DIR]")
	}
	store, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()
	if err := backup.Run(ctx, store, pos[0]); err != nil {
		return err
	}
	fmt.Println(pos[0])
	return nil
}
