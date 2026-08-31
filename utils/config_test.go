package utils

import (
	"testing"

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
