// Command f95-tracker is the single binary: serve, check, import-csv, fixture,
// backup and version. Each subcommand lives in its own cmd_<name>.go.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

var errNotImplemented = errors.New("not implemented yet")

// errUsage marks a command-line mistake (exit 2).
type errUsage string

func (e errUsage) Error() string { return string(e) }

var commands = map[string]func(ctx context.Context, args []string) error{
	"serve":      runServe,
	"check":      runCheck,
	"import-csv": runImport,
	"fixture":    runFixture,
	"backup":     runBackup,
	"version":    runVersion,
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: f95-tracker <serve|check|import-csv|fixture|backup|version> [flags]")
		return 2
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "f95-tracker: unknown command %q\n", args[0])
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := cmd(ctx, args[1:])
	var usage errUsage
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errNotImplemented):
		fmt.Fprintf(os.Stderr, "f95-tracker %s: not implemented yet\n", args[0])
		return 2
	case errors.As(err, &usage):
		fmt.Fprintf(os.Stderr, "f95-tracker %s: %v\n", args[0], err)
		return 2
	default:
		fmt.Fprintf(os.Stderr, "f95-tracker %s: %v\n", args[0], err)
		return 1
	}
}

// newLogger returns the JSON-to-stderr logger used by every long-running command.
func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
