package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// env returns a LookupEnv over a fixed set of variables.
func env(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	}
}

// writeFile writes a file with the given mode into a new directory and
// returns the directory.
func writeFile(t *testing.T, name, body string, mode os.FileMode) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, name)

	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("failed to write %s: %v", name, err)
	}

	// WriteFile's mode is filtered by the umask; set it exactly.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("failed to chmod %s: %v", name, err)
	}

	return dir
}

// TestLoadLayersOverrideInOrder is the API-5 precedence test: a file beats the
// defaults, the environment beats the file, and a flag beats the environment.
func TestLoadLayersOverrideInOrder(t *testing.T) {
	t.Parallel()

	dir := writeFile(t, ConfigFileName, `{
		"http_port": 7001, "ws_port": 7002,
		"logging": {"level": "WARN"},
		"sync_client": {"username": "file-user", "password": "file-pass"}
	}`, 0o600)

	loaded, err := Load(LoadOptions{
		Dir: dir,
		LookupEnv: env(map[string]string{
			"TESTSYNC_WS_PORT":                      "7102",
			"TESTSYNC_LOGGING_LEVEL":                "ERROR",
			"TESTSYNC_CLEANUP_RETENTION":            "2h",
			"TESTSYNC_LIMITS_MAX_DATA_BYTES":        "1024",
			"TESTSYNC_SYNC_CLIENT_PASSWORD":         "env-pass",
			"TESTSYNC_CHECKPOINT_RELEASE_LEAD_TIME": "750ms",
		}),
		Override: func(c *Config) { c.Logging.Level = "DEBUG" },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c := loaded.Config

	checks := map[string]bool{
		"file beats default (http_port)":      c.HTTPPort == 7001,
		"env beats file (ws_port)":            c.WSPort == 7102,
		"flag beats env (logging.level)":      c.Logging.Level == "DEBUG",
		"env sets a duration":                 c.Cleanup.Retention.Duration() == 2*time.Hour,
		"env sets an int64":                   c.Limits.MaxDataBytes == 1024,
		"env sets a nested duration":          c.Checkpoint.ReleaseLeadTime.Duration() == 750*time.Millisecond,
		"env password beats file password":    c.SyncClient.Password == "env-pass",
		"file username survives":              c.SyncClient.Username == "file-user",
		"untouched settings keep the default": c.Cleanup.Interval.Duration() == DefaultCleanupInterval,
	}

	for name, ok := range checks {
		if !ok {
			t.Errorf("%s: got %+v", name, c)
		}
	}

	if loaded.File != filepath.Join(dir, ConfigFileName) {
		t.Errorf("unexpected file %q", loaded.File)
	}
}

// TestLoadWithoutAFile covers the missing file: fine where nobody asked for
// one, an error where the operator pointed -c at it.
func TestLoadWithoutAFile(t *testing.T) {
	t.Parallel()

	loaded, err := Load(LoadOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("an optional missing file must not be an error: %v", err)
	}

	if loaded.File != "" || loaded.Config.HTTPPort != DefaultHTTPPort {
		t.Fatalf("expected defaults and no file, got %+v", loaded)
	}

	if _, err := Load(LoadOptions{Dir: t.TempDir(), RequireFile: true}); err == nil ||
		!strings.Contains(err.Error(), "no configuration file") {
		t.Fatalf("expected a required missing file to be an error, got %v", err)
	}
}

// TestLoadEnvErrorsNameTheVariable covers a bad value from the environment:
// the operator is told which variable, since there is no file line to point
// at.
func TestLoadEnvErrorsNameTheVariable(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]string{
		"TESTSYNC_HTTP_PORT":         "eighty",
		"TESTSYNC_CLEANUP_RETENTION": "soon",
	} {
		_, err := Load(LoadOptions{Dir: t.TempDir(), LookupEnv: env(map[string]string{name: value})})
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=%s: expected an error naming the variable, got %v", name, value, err)
		}
	}

	// A value that parses but is not usable is still caught by validation.
	_, err := Load(LoadOptions{
		Dir: t.TempDir(), LookupEnv: env(map[string]string{"TESTSYNC_HTTP_PORT": "70000"}),
	})
	if err == nil || !strings.Contains(err.Error(), "http_port is 70000") {
		t.Errorf("expected the port to be validated, got %v", err)
	}
}

// TestLoadIgnoresEmptyVariables covers a variable defined with no value, which
// compose files and CI templates produce routinely: it must not wipe the
// file's setting.
func TestLoadIgnoresEmptyVariables(t *testing.T) {
	t.Parallel()

	dir := writeFile(t, ConfigFileName, `{"http_port": 7001, "ws_port": 7002}`, 0o600)

	loaded, err := Load(LoadOptions{
		Dir: dir, LookupEnv: env(map[string]string{"TESTSYNC_HTTP_PORT": ""}),
	})
	if err != nil || loaded.Config.HTTPPort != 7001 {
		t.Fatalf("an empty variable overrode the file: %d, %v", loaded.Config.HTTPPort, err)
	}
}

// TestLoadWarnsAboutUnknownKeys covers a typo in the file, which used to be
// ignored without a word: the setting silently kept its default.
func TestLoadWarnsAboutUnknownKeys(t *testing.T) {
	t.Parallel()

	dir := writeFile(t, ConfigFileName,
		`{"htp_port": 1, "logging": {"levle": "DEBUG"}, "storage": {"sqlite_path": "x.db"}}`, 0o600)

	loaded, err := Load(LoadOptions{Dir: dir})
	if err != nil {
		t.Fatalf("unknown keys must not stop the server: %v", err)
	}

	joined := strings.Join(loaded.Warnings, "\n")

	for _, key := range []string{`"htp_port"`, `"logging.levle"`} {
		if !strings.Contains(joined, key) {
			t.Errorf("no warning for %s in:\n%s", key, joined)
		}
	}

	if strings.Contains(joined, "sqlite_path") {
		t.Errorf("a known key was reported as unknown:\n%s", joined)
	}
}

// TestLoadReadsThePasswordFile covers sync_client.password_file, the SEC-6
// path for Docker and Kubernetes secrets: the trailing newline every editor
// adds is dropped and the username defaults. A secret mounted 0644, as
// Kubernetes does, is not warned about.
func TestLoadReadsThePasswordFile(t *testing.T) {
	t.Parallel()

	secrets := writeFile(t, "testsync", "pass word\n", 0o644)

	loaded, err := Load(LoadOptions{
		Dir: t.TempDir(),
		LookupEnv: env(map[string]string{
			"TESTSYNC_SYNC_CLIENT_PASSWORD_FILE": filepath.Join(secrets, "testsync"),
		}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	creds := loaded.Config.SyncClient

	if creds.Password != "pass word" || creds.Username != DefaultUsername || creds.PasswordFile != "" {
		t.Fatalf("unexpected credentials: user %q, password %q, file %q",
			creds.Username, creds.Password, creds.PasswordFile)
	}

	if len(loaded.Warnings) != 0 {
		t.Fatalf("a mounted secret was warned about: %v", loaded.Warnings)
	}
}

// TestLoadPasswordSources covers the two ways to give a password meeting: in
// one layer they conflict, across layers the higher one wins outright.
func TestLoadPasswordSources(t *testing.T) {
	t.Parallel()

	secrets := writeFile(t, "secret", "from-file", 0o600)
	secretPath := filepath.Join(secrets, "secret")

	both := writeFile(t, ConfigFileName,
		`{"sync_client": {"password": "p", "password_file": "`+secretPath+`"}}`, 0o600)

	if _, err := Load(LoadOptions{Dir: both}); err == nil || !strings.Contains(err.Error(), "keep one") {
		t.Fatalf("expected both in one file to conflict, got %v", err)
	}

	fileNamesSecret := writeFile(t, ConfigFileName,
		`{"sync_client": {"password_file": "`+secretPath+`"}}`, 0o600)

	loaded, err := Load(LoadOptions{
		Dir:       fileNamesSecret,
		LookupEnv: env(map[string]string{"TESTSYNC_SYNC_CLIENT_PASSWORD": "from-env"}),
	})
	if err != nil || loaded.Config.SyncClient.Password != "from-env" {
		t.Fatalf("the environment's password did not replace the file's secret: %q, %v",
			loaded.Config.SyncClient.Password, err)
	}

	empty := writeFile(t, "empty", "\n", 0o600)

	_, err = Load(LoadOptions{
		Dir: t.TempDir(),
		LookupEnv: env(map[string]string{
			"TESTSYNC_SYNC_CLIENT_PASSWORD_FILE": filepath.Join(empty, "empty"),
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("expected an empty secret to be refused, got %v", err)
	}
}

// TestLoadWarnsAboutAWorldReadablePassword covers the plaintext password in a
// file anyone on the machine can read.
func TestLoadWarnsAboutAWorldReadablePassword(t *testing.T) {
	t.Parallel()

	dir := writeFile(t, ConfigFileName, `{"sync_client": {"username": "u", "password": "p"}}`, 0o644)

	loaded, err := Load(LoadOptions{Dir: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(strings.Join(loaded.Warnings, "\n"), "chmod 600") {
		t.Fatalf("expected a permissions warning, got %v", loaded.Warnings)
	}

	private := writeFile(t, ConfigFileName, `{"sync_client": {"username": "u", "password": "p"}}`, 0o600)

	if loaded, _ := Load(LoadOptions{Dir: private}); len(loaded.Warnings) != 0 {
		t.Fatalf("a private file was warned about: %v", loaded.Warnings)
	}
}

// TestEveryFileKeyHasAVariable pins the naming rule the documentation states,
// and checks that every setting can be reached from the environment.
func TestEveryFileKeyHasAVariable(t *testing.T) {
	t.Parallel()

	vars := EnvVars()

	for name, key := range map[string]string{
		"TESTSYNC_HTTP_PORT":                       "http_port",
		"TESTSYNC_SYNC_CLIENT_PASSWORD":            "sync_client.password",
		"TESTSYNC_SYNC_CLIENT_PASSWORD_FILE":       "sync_client.password_file",
		"TESTSYNC_STORAGE_SQLITE_PATH":             "storage.sqlite_path",
		"TESTSYNC_LOGGING_LEVEL":                   "logging.level",
		"TESTSYNC_LIMITS_MAX_CONNECTIONS_PER_TEST": "limits.max_connections_per_test",
		"TESTSYNC_CHECKPOINT_RELEASE_LEAD_TIME":    "checkpoint.release_lead_time",
	} {
		if vars[name] != key {
			t.Errorf("%s: maps to %q, want %q", name, vars[name], key)
		}
	}

	// Every leaf is settable: setField would refuse a kind it cannot parse.
	var conf Config
	for _, s := range settings(&conf) {
		if err := setField(s.field, "1s"); err != nil && strings.Contains(err.Error(), "cannot be set") {
			t.Errorf("%s cannot be set from the environment", s.env)
		}
	}
}
