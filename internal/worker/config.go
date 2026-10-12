package worker

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the CLI configuration (flag > env > default).
type Config struct {
	TrackerURL   string
	GamesDir     string
	StateDir     string
	PollInterval time.Duration
	LogLevel     slog.Level
	Token        string
}

// LoadConfig parses args (without argv[0]). The token is read from
// $CREDENTIALS_DIRECTORY/downloader-token, else F95_DOWNLOADER_TOKEN_FILE.
func LoadConfig(args []string, getenv func(string) string) (Config, error) {
	var c Config
	fs := flag.NewFlagSet("f95-downloader", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	def := func(env, d string) string {
		if v := getenv(env); v != "" {
			return v
		}
		return d
	}
	fs.StringVar(&c.TrackerURL, "tracker-url", def("F95_DOWNLOADER_TRACKER_URL", ""), "tracker base URL")
	fs.StringVar(&c.GamesDir, "games-dir", def("F95_DOWNLOADER_GAMES_DIR", ""), "games directory")
	fs.StringVar(&c.StateDir, "state-dir", def("F95_DOWNLOADER_STATE_DIR", ""), "state directory")
	poll := fs.String("poll-interval", def("F95_DOWNLOADER_POLL_INTERVAL", "60s"), "poll interval")
	level := fs.String("log-level", def("F95_DOWNLOADER_LOG_LEVEL", "info"), "log level")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	var errs []error
	for name, v := range map[string]string{"tracker-url": c.TrackerURL, "games-dir": c.GamesDir, "state-dir": c.StateDir} {
		if v == "" {
			errs = append(errs, fmt.Errorf("--%s is required", name))
		}
	}
	d, err := time.ParseDuration(*poll)
	if err != nil || d <= 0 {
		errs = append(errs, fmt.Errorf("bad --poll-interval %q", *poll))
	}
	c.PollInterval = d
	if err := c.LogLevel.UnmarshalText([]byte(strings.ToLower(*level))); err != nil {
		errs = append(errs, fmt.Errorf("bad --log-level %q", *level))
	}
	file := getenv("F95_DOWNLOADER_TOKEN_FILE")
	if dir := getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		if p := filepath.Join(dir, "downloader-token"); fileExists(p) {
			file = p
		}
	}
	if file == "" {
		errs = append(errs, errors.New("no token: set CREDENTIALS_DIRECTORY/downloader-token or F95_DOWNLOADER_TOKEN_FILE"))
	} else if b, err := os.ReadFile(file); err != nil {
		errs = append(errs, fmt.Errorf("read token: %w", err))
	} else if c.Token = strings.TrimSpace(string(b)); c.Token == "" {
		errs = append(errs, errors.New("token file is empty"))
	}
	return c, errors.Join(errs...)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
