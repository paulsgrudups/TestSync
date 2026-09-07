package main

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/utils"
)

// captureLog returns a logger writing into a buffer the test can assert on,
// at WARN so that the startup banner is the only thing in it.
func captureLog(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()

	buf := &bytes.Buffer{}

	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	return logger, buf
}

// TestSetupAuthRequiresCredentials covers SEC-1: startup with no credentials
// and no explicit opt-out is a fatal error, not a silently open server.
func TestSetupAuthRequiresCredentials(t *testing.T) {
	logger, _ := captureLog(t)

	conf := utils.Config{}
	utils.ApplyDefaults(&conf)

	validator, err := setupAuth(conf, false, logger)
	if !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("expected ErrNoCredentials, got %v", err)
	}

	if validator.Validate("attacker", "guess") {
		t.Fatal("a failed auth setup left the server open")
	}
}

// TestSetupAuthBuildsValidator covers the configured case: one validator is
// built for the App, and both the HTTP and the WebSocket path use it.
func TestSetupAuthBuildsValidator(t *testing.T) {
	logger, _ := captureLog(t)

	conf := utils.Config{
		SyncClient: utils.BasicCredentials{Username: "user", Password: "pass"},
	}
	utils.ApplyDefaults(&conf)

	validator, err := setupAuth(conf, false, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if validator.Disabled() {
		t.Fatal("authentication must not be disabled for a configured credential")
	}

	if !validator.Validate("user", "pass") {
		t.Fatal("the configured credential was rejected")
	}

	if validator.Validate("user", "nope") || validator.Validate("nope", "pass") {
		t.Fatal("a wrong credential was accepted")
	}
}

// TestSetupAuthOptOutWarns covers the explicit opt-out: it starts, and it says
// so loudly on every startup (SEC-1).
func TestSetupAuthOptOutWarns(t *testing.T) {
	cases := map[string]struct {
		conf     utils.Config
		insecure bool
	}{
		"config mode none": {
			conf:     utils.Config{Auth: utils.AuthConfig{Mode: utils.AuthModeNone}},
			insecure: false,
		},
		"insecure flag": {conf: utils.Config{}, insecure: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			logger, logs := captureLog(t)

			conf := tc.conf
			utils.ApplyDefaults(&conf)

			validator, err := setupAuth(conf, tc.insecure, logger)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !validator.Disabled() {
				t.Fatal("expected authentication to be disabled")
			}

			if !validator.Validate("", "") {
				t.Fatal("expected a credential-less caller to be accepted")
			}

			if !bytes.Contains(logs.Bytes(), []byte("AUTHENTICATION IS DISABLED")) {
				t.Fatalf("expected a warning banner, got: %s", logs.String())
			}

			if !bytes.Contains(logs.Bytes(), []byte("level=WARN")) {
				t.Fatalf("expected the banner to be logged at WARN, got: %s", logs.String())
			}
		})
	}
}

// TestSetupAuthUnknownMode covers a typo in the auth mode: it must fail loudly
// rather than fall back to something permissive.
func TestSetupAuthUnknownMode(t *testing.T) {
	logger, _ := captureLog(t)

	conf := utils.Config{
		Auth:       utils.AuthConfig{Mode: "off"},
		SyncClient: utils.BasicCredentials{Username: "user", Password: "pass"},
	}

	validator, err := setupAuth(conf, false, logger)
	if !errors.Is(err, auth.ErrUnknownAuthMode) {
		t.Fatalf("expected ErrUnknownAuthMode, got %v", err)
	}

	if validator.Validate("user", "pass") {
		t.Fatal("a failed auth setup returned a usable validator")
	}
}
