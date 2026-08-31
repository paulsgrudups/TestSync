package main

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/utils"

	log "github.com/sirupsen/logrus"
)

// captureLog redirects the global logger for the duration of a test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()

	buf := &bytes.Buffer{}
	previousLevel := log.GetLevel()

	log.SetOutput(buf)
	log.SetLevel(log.WarnLevel)

	t.Cleanup(func() {
		log.SetOutput(io.Discard)
		log.SetLevel(previousLevel)
	})

	return buf
}

// resetSharedValidator restores the process-wide validator after a test.
func resetSharedValidator(t *testing.T) {
	t.Helper()

	previous := auth.Shared()
	t.Cleanup(func() { auth.SetShared(previous) })

	auth.SetShared(nil)
}

// TestSetupAuthRequiresCredentials covers SEC-1: startup with no credentials
// and no explicit opt-out is a fatal error, not a silently open server.
func TestSetupAuthRequiresCredentials(t *testing.T) {
	captureLog(t)
	resetSharedValidator(t)

	conf := utils.Config{}
	utils.ApplyDefaults(&conf)

	err := setupAuth(conf, false)
	if !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("expected ErrNoCredentials, got %v", err)
	}

	if auth.Shared().Validate("attacker", "guess") {
		t.Fatal("a failed auth setup left the server open")
	}
}

// TestSetupAuthInstallsSharedValidator covers the configured case: one
// validator serves both the HTTP and the WebSocket path.
func TestSetupAuthInstallsSharedValidator(t *testing.T) {
	captureLog(t)
	resetSharedValidator(t)

	conf := utils.Config{
		SyncClient: utils.BasicCredentials{Username: "user", Password: "pass"},
	}
	utils.ApplyDefaults(&conf)

	if err := setupAuth(conf, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	validator := auth.Shared()
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
			logs := captureLog(t)
			resetSharedValidator(t)

			conf := tc.conf
			utils.ApplyDefaults(&conf)

			if err := setupAuth(conf, tc.insecure); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !auth.Shared().Disabled() {
				t.Fatal("expected authentication to be disabled")
			}

			if !auth.Shared().Validate("", "") {
				t.Fatal("expected a credential-less caller to be accepted")
			}

			if !bytes.Contains(logs.Bytes(), []byte("AUTHENTICATION IS DISABLED")) {
				t.Fatalf("expected a warning banner, got: %s", logs.String())
			}

			if !bytes.Contains(logs.Bytes(), []byte("level=warning")) {
				t.Fatalf("expected the banner to be logged at WARN, got: %s", logs.String())
			}
		})
	}
}

// TestSetupAuthUnknownMode covers a typo in the auth mode: it must fail loudly
// rather than fall back to something permissive.
func TestSetupAuthUnknownMode(t *testing.T) {
	captureLog(t)
	resetSharedValidator(t)

	conf := utils.Config{
		Auth:       utils.AuthConfig{Mode: "off"},
		SyncClient: utils.BasicCredentials{Username: "user", Password: "pass"},
	}

	if err := setupAuth(conf, false); !errors.Is(err, auth.ErrUnknownAuthMode) {
		t.Fatalf("expected ErrUnknownAuthMode, got %v", err)
	}

	if auth.Shared().Validate("user", "pass") {
		t.Fatal("a failed auth setup installed a validator")
	}
}
