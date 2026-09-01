package utils

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

func TestReadConfig_Success(t *testing.T) {
	originalFS := FS
	FS = afero.NewMemMapFs()
	t.Cleanup(func() { FS = originalFS })

	contents := `{
        "http_port": 9104,
        "ws_port": 9105,
        "logging": {"level": "DEBUG", "dir": "./logs"},
        "sync_client": {"username": "user", "password": "pass"}
    }`

	if err := afero.WriteFile(FS, "/config.json", []byte(contents), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	var cfg Config
	if err := ReadConfig("/config.json", &cfg); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if cfg.HTTPPort != 9104 || cfg.WSPort != 9105 {
		t.Fatalf("unexpected ports: http=%d ws=%d", cfg.HTTPPort, cfg.WSPort)
	}

	if cfg.Logging.Level != "DEBUG" || cfg.Logging.Dir != "./logs" {
		t.Fatalf("unexpected logging config: %+v", cfg.Logging)
	}

	if cfg.SyncClient.Username != "user" || cfg.SyncClient.Password != "pass" {
		t.Fatalf("unexpected sync client config: %+v", cfg.SyncClient)
	}
}

func TestReadConfig_InvalidJSON(t *testing.T) {
	originalFS := FS
	FS = afero.NewMemMapFs()
	t.Cleanup(func() { FS = originalFS })

	if err := afero.WriteFile(FS, "/config.json", []byte("{"), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	var cfg Config
	if err := ReadConfig("/config.json", &cfg); err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestApplyDefaults(t *testing.T) {
	var cfg Config
	ApplyDefaults(&cfg)

	if cfg.Logging.Level != "INFO" {
		t.Fatalf("expected default log level INFO, got %q", cfg.Logging.Level)
	}
	if cfg.Logging.Dir != "." {
		t.Fatalf("expected default log dir '.', got %q", cfg.Logging.Dir)
	}
	if cfg.Storage.SQLitePath != DefaultSQLitePath {
		t.Fatalf("expected default sqlite path %q, got %q", DefaultSQLitePath, cfg.Storage.SQLitePath)
	}
	// Authentication defaults to required: disabling it has to be an explicit
	// choice (SEC-1).
	if cfg.Auth.Mode != AuthModeBasic {
		t.Fatalf("expected default auth mode %q, got %q", AuthModeBasic, cfg.Auth.Mode)
	}
}

func TestReadConfig_AuthMode(t *testing.T) {
	originalFS := FS
	FS = afero.NewMemMapFs()
	t.Cleanup(func() { FS = originalFS })

	contents := `{"auth": {"mode": "none"}}`

	if err := afero.WriteFile(FS, "/config.json", []byte(contents), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	var cfg Config
	if err := ReadConfig("/config.json", &cfg); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if cfg.Auth.Mode != AuthModeNone {
		t.Fatalf("expected auth mode %q, got %q", AuthModeNone, cfg.Auth.Mode)
	}

	ApplyDefaults(&cfg)

	if cfg.Auth.Mode != AuthModeNone {
		t.Fatalf("defaults overwrote the configured auth mode: %q", cfg.Auth.Mode)
	}
}

// TestApplyDefaults_CleanupAndLimits covers the settings added for STAB-3 and
// STAB-5: an omitted section leaves a bounded, sweeping server rather than an
// unbounded one.
func TestApplyDefaults_CleanupAndLimits(t *testing.T) {
	conf := Config{}
	ApplyDefaults(&conf)

	if conf.Cleanup.Interval.Duration() != DefaultCleanupInterval {
		t.Fatalf("unexpected cleanup interval: %s", conf.Cleanup.Interval.Duration())
	}

	if conf.Cleanup.Retention.Duration() != DefaultRetention {
		t.Fatalf("unexpected retention: %s", conf.Cleanup.Retention.Duration())
	}

	if conf.Limits.MaxTests != DefaultMaxTests ||
		conf.Limits.MaxConnectionsPerTest != DefaultMaxConnectionsPerTest ||
		conf.Limits.MaxCheckpointsPerTest != DefaultMaxCheckpointsPerTest ||
		conf.Limits.MaxDataBytes != DefaultMaxDataBytes {
		t.Fatalf("unexpected limits: %+v", conf.Limits)
	}

	if err := Validate(&conf); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
}

// TestDurationUnmarshal covers the configured duration strings, including the
// errors an operator can meet.
func TestDurationUnmarshal(t *testing.T) {
	cases := map[string]struct {
		body    string
		want    time.Duration
		wantErr string
	}{
		"hours":  {body: `"12h"`, want: 12 * time.Hour},
		"millis": {body: `"500ms"`, want: 500 * time.Millisecond},
		"empty":  {body: `""`, want: 0},
		"not a string": {
			body:    `12`,
			wantErr: "duration must be a string",
		},
		"nonsense": {
			body:    `"soon"`,
			wantErr: `invalid duration "soon"`,
		},
		"negative": {
			body:    `"-5m"`,
			wantErr: `duration "-5m" must be positive`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var got Duration

			err := json.Unmarshal([]byte(tc.body), &got)

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected an error mentioning %q, got %v", tc.wantErr, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.Duration() != tc.want {
				t.Fatalf("expected %s, got %s", tc.want, got.Duration())
			}
		})
	}
}
