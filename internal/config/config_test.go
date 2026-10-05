package config

import (
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func writeCred(t *testing.T, dir, name, val string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(val), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func serveEnv(t *testing.T) map[string]string {
	d := t.TempDir()
	return map[string]string{
		"F95_TRACKER_STATE_DIR":               "/state",
		"F95_TRACKER_BASE_URL":                "https://f95.example/",
		"F95_TRACKER_OIDC_ISSUER":             "https://auth.example",
		"F95_TRACKER_OIDC_CLIENT_ID":          "cid",
		"F95_TRACKER_OIDC_ALLOWED_SUBJECTS":   " a, b ,,c",
		"F95_TRACKER_NTFY_URL":                "https://ntfy.example",
		"F95_TRACKER_NTFY_TOPIC":              "t",
		"F95_TRACKER_OIDC_CLIENT_SECRET_FILE": writeCred(t, d, "o", "osecret\n"),
		"F95_TRACKER_SESSION_SECRET_FILE":     writeCred(t, d, "s", "sess"),
		"F95_TRACKER_NTFY_TOKEN_FILE":         writeCred(t, d, "n", "tok"),
	}
}

func TestServeFullConfig(t *testing.T) {
	c, pos, err := Load("serve", nil, envMap(serveEnv(t)))
	if err != nil {
		t.Fatal(err)
	}
	if len(pos) != 0 || c.StateDir != "/state" || c.Listen != DefaultListen || c.F95BaseURL != DefaultF95Base ||
		c.BaseURL != "https://f95.example" || c.LogLevel != slog.LevelInfo {
		t.Fatalf("unexpected: %+v", c)
	}
	if !reflect.DeepEqual(c.OIDCAllowedSubjects, []string{"a", "b", "c"}) {
		t.Fatalf("subjects: %v", c.OIDCAllowedSubjects)
	}
	if c.OIDCClientSecret.Reveal() != "osecret" || c.SessionSecret.Reveal() != "sess" || c.NtfyToken.Reveal() != "tok" {
		t.Fatal("credentials not read/trimmed")
	}
	if got := c.DBPath(); got != "/state/f95-tracker.db" {
		t.Fatalf("DBPath %q", got)
	}
	if strings.Contains(slogString(c.OIDCClientSecret), "osecret") {
		t.Fatal("secret leaked")
	}
}

func slogString(s Secret) string { return s.String() + s.GoString() + s.LogValue().String() }

func TestPrecedenceFlagOverEnvOverDefault(t *testing.T) {
	env := serveEnv(t)
	env["F95_TRACKER_LISTEN"] = "0.0.0.0:1"
	env["F95_TRACKER_LOG_LEVEL"] = "warn"
	c, _, err := Load("serve", nil, envMap(env))
	if err != nil || c.Listen != "0.0.0.0:1" || c.LogLevel != slog.LevelWarn {
		t.Fatalf("env: %+v %v", c, err)
	}
	c, _, err = Load("serve", []string{"--listen", "127.0.0.1:9", "--log-level=debug", "--state-dir", "/flag", "--base-url", "http://x"}, envMap(env))
	if err != nil || c.Listen != "127.0.0.1:9" || c.LogLevel != slog.LevelDebug || c.StateDir != "/flag" || c.BaseURL != "http://x" {
		t.Fatalf("flag: %+v %v", c, err)
	}
}

func TestRequiredPerSubcommand(t *testing.T) {
	empty := envMap(nil)
	if _, _, err := Load("serve", nil, empty); err == nil {
		t.Fatal("serve with nothing must fail")
	} else {
		for _, want := range []string{"state-dir", "base-url", "OIDC_ISSUER", "OIDC_CLIENT_ID", "ALLOWED_SUBJECTS", "NTFY_URL", "NTFY_TOPIC", "oidc-client-secret", "session-secret", "ntfy-token"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error lacks %q: %v", want, err)
			}
		}
	}
	// check: base URL + ntfy (+ token), no OIDC or session secret.
	env := serveEnv(t)
	for _, k := range []string{"F95_TRACKER_OIDC_ISSUER", "F95_TRACKER_OIDC_CLIENT_ID", "F95_TRACKER_OIDC_ALLOWED_SUBJECTS", "F95_TRACKER_OIDC_CLIENT_SECRET_FILE", "F95_TRACKER_SESSION_SECRET_FILE"} {
		delete(env, k)
	}
	if _, _, err := Load("check", nil, envMap(env)); err != nil {
		t.Fatalf("check: %v", err)
	}
	if _, _, err := Load("serve", nil, envMap(env)); err == nil {
		t.Fatal("serve must still need OIDC")
	}
	delete(env, "F95_TRACKER_NTFY_TOPIC")
	if _, _, err := Load("check", nil, envMap(env)); err == nil {
		t.Fatal("check needs ntfy topic")
	}
	// backup / import-csv / fixture: only the state dir.
	if _, _, err := Load("backup", []string{"--state-dir", "/s"}, empty); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, _, err := Load("backup", nil, empty); err == nil {
		t.Fatal("backup needs state dir")
	}
}

func TestCredentialsDirectoryWinsOverFileEnv(t *testing.T) {
	env := serveEnv(t)
	dir := t.TempDir()
	writeCred(t, dir, "ntfy-token", "from-dir")
	env["CREDENTIALS_DIRECTORY"] = dir
	c, _, err := Load("serve", nil, envMap(env))
	if err != nil || c.NtfyToken.Reveal() != "from-dir" || c.SessionSecret.Reveal() != "sess" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestPositionalAndExtraFlagsInterspersed(t *testing.T) {
	var noBackfill bool
	c, pos, err := Load("import-csv", []string{"data.csv", "--no-backfill", "--state-dir", "/s"}, envMap(nil),
		func(fs *flag.FlagSet) { fs.BoolVar(&noBackfill, "no-backfill", false, "") })
	if err != nil || !noBackfill || c.StateDir != "/s" || !reflect.DeepEqual(pos, []string{"data.csv"}) {
		t.Fatalf("%+v %v %v %v", c, pos, noBackfill, err)
	}
}

func TestInvalidValues(t *testing.T) {
	env := serveEnv(t)
	env["F95_TRACKER_BASE_URL"] = "not a url"
	if _, _, err := Load("serve", nil, envMap(env)); err == nil {
		t.Fatal("bad base URL")
	}
	if _, _, err := Load("serve", []string{"--log-level", "loud"}, envMap(serveEnv(t))); err == nil {
		t.Fatal("bad log level")
	}
	if _, _, err := Load("nope", nil, envMap(nil)); err == nil {
		t.Fatal("unknown subcommand")
	}
}
