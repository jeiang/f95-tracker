// Package config is the only package that reads flags and the environment
// (spec §5.1). Precedence: flag > env > default. Secrets are never flags or plain
// env: they are files in $CREDENTIALS_DIRECTORY/<name>, falling back to the file
// named by F95_TRACKER_<NAME>_FILE.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultListen   = "127.0.0.1:8470"
	DefaultF95Base  = "https://f95zone.to"
	DefaultLogLevel = "info"
	credOIDCSecret  = "oidc-client-secret"
	credSessionKey  = "session-secret"
	credNtfyToken   = "ntfy-token"
	envPrefix       = "F95_TRACKER_"
	dbFileName      = "f95-tracker.db"
	subcommandServe = "serve"
	subcommandCheck = "check"
)

// Secret holds a credential value and never prints it.
type Secret string

func (Secret) String() string       { return "[redacted]" }
func (Secret) GoString() string     { return "[redacted]" }
func (Secret) LogValue() slog.Value { return slog.StringValue("[redacted]") }
func (s Secret) Reveal() string     { return string(s) }
func (s Secret) IsSet() bool        { return s != "" }

type Config struct {
	StateDir string
	Listen   string
	BaseURL  string // no trailing slash
	LogLevel slog.Level

	OIDCIssuer          string
	OIDCClientID        string
	OIDCAllowedSubjects []string

	NtfyURL   string
	NtfyTopic string

	F95BaseURL string

	OIDCClientSecret Secret
	SessionSecret    Secret
	NtfyToken        Secret
}

func (c Config) DBPath() string        { return filepath.Join(c.StateDir, dbFileName) }
func (c Config) CoversDir() string     { return filepath.Join(c.StateDir, "covers") }
func (c Config) F95LockPath() string   { return filepath.Join(c.StateDir, "f95.lock") }
func (c Config) CheckLockPath() string { return filepath.Join(c.StateDir, "check.lock") }
func (c Config) BackupDir() string     { return filepath.Join(c.StateDir, "backup") }

// Load resolves the configuration for a subcommand and returns the positional
// arguments. Flags and positionals may be interspersed. extra registers
// subcommand-specific flags (e.g. check --no-digest) on the same FlagSet; their
// values are read by the caller. getenv is os.Getenv in production.
func Load(sub string, args []string, getenv func(string) string, extra ...func(*flag.FlagSet)) (Config, []string, error) {
	var c Config
	switch sub {
	case subcommandServe, subcommandCheck, "import-csv", "fixture", "backup":
	default:
		return c, nil, fmt.Errorf("config: unknown subcommand %q", sub)
	}

	env := func(name string) string { return getenv(envPrefix + name) }
	orDefault := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}

	fs := flag.NewFlagSet(sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateDir := fs.String("state-dir", env("STATE_DIR"), "state directory (F95_TRACKER_STATE_DIR)")
	logLevel := fs.String("log-level", orDefault(env("LOG_LEVEL"), DefaultLogLevel), "debug, info, warn or error (F95_TRACKER_LOG_LEVEL)")
	baseURL := fs.String("base-url", env("BASE_URL"), "public base URL (F95_TRACKER_BASE_URL)")
	var listen *string
	if sub == subcommandServe {
		listen = fs.String("listen", orDefault(env("LISTEN"), DefaultListen), "listen address (F95_TRACKER_LISTEN)")
	}
	for _, f := range extra {
		f(fs)
	}

	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return c, nil, fmt.Errorf("config: %w", err)
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}

	c.StateDir = *stateDir
	c.F95BaseURL = strings.TrimRight(orDefault(env("F95_BASE_URL"), DefaultF95Base), "/")
	c.BaseURL = strings.TrimRight(*baseURL, "/")
	if listen != nil {
		c.Listen = *listen
	}

	var errs []error
	missing := func(what string) { errs = append(errs, fmt.Errorf("%s is required", what)) }

	if err := c.LogLevel.UnmarshalText([]byte(*logLevel)); err != nil {
		errs = append(errs, fmt.Errorf("invalid log level %q", *logLevel))
	}
	if c.StateDir == "" {
		missing("--state-dir / F95_TRACKER_STATE_DIR")
	}

	if sub == subcommandServe || sub == subcommandCheck {
		if c.BaseURL == "" {
			missing("--base-url / F95_TRACKER_BASE_URL")
		} else if u, err := url.Parse(c.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("base URL %q is not an absolute http(s) URL", c.BaseURL))
		}
		c.NtfyURL = env("NTFY_URL")
		c.NtfyTopic = env("NTFY_TOPIC")
		if c.NtfyURL == "" {
			missing("F95_TRACKER_NTFY_URL")
		}
		if c.NtfyTopic == "" {
			missing("F95_TRACKER_NTFY_TOPIC")
		}
		c.NtfyToken, errs = credential(credNtfyToken, getenv, errs)
	}
	if sub == subcommandServe {
		c.OIDCIssuer = env("OIDC_ISSUER")
		c.OIDCClientID = env("OIDC_CLIENT_ID")
		for _, s := range strings.Split(env("OIDC_ALLOWED_SUBJECTS"), ",") {
			if s = strings.TrimSpace(s); s != "" {
				c.OIDCAllowedSubjects = append(c.OIDCAllowedSubjects, s)
			}
		}
		if c.OIDCIssuer == "" {
			missing("F95_TRACKER_OIDC_ISSUER")
		}
		if c.OIDCClientID == "" {
			missing("F95_TRACKER_OIDC_CLIENT_ID")
		}
		if len(c.OIDCAllowedSubjects) == 0 {
			missing("F95_TRACKER_OIDC_ALLOWED_SUBJECTS")
		}
		c.OIDCClientSecret, errs = credential(credOIDCSecret, getenv, errs)
		c.SessionSecret, errs = credential(credSessionKey, getenv, errs)
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, nil, fmt.Errorf("config: %w", err)
	}
	return c, positional, nil
}

// credential reads a required credential file; problems are appended to errs.
func credential(name string, getenv func(string) string, errs []error) (Secret, []error) {
	envName := envPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_FILE"
	var path string
	if dir := getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		if p := filepath.Join(dir, name); fileExists(p) {
			path = p
		}
	}
	if path == "" {
		path = getenv(envName)
	}
	if path == "" {
		return "", append(errs, fmt.Errorf("credential %q is required ($CREDENTIALS_DIRECTORY/%s or %s)", name, name, envName))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", append(errs, fmt.Errorf("credential %q: %w", name, err))
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", append(errs, fmt.Errorf("credential %q is empty", name))
	}
	return Secret(v), errs
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return !errors.Is(err, fs.ErrNotExist)
}
