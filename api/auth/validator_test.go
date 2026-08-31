package auth

import (
	"errors"
	"testing"

	"github.com/paulsgrudups/testsync/utils"
)

// TestNewValidatorRequiresCredentials covers SEC-1: empty credentials used to
// authenticate every caller, and must now be refused outright.
func TestNewValidatorRequiresCredentials(t *testing.T) {
	cases := map[string]utils.BasicCredentials{
		"both empty":     {Username: "", Password: ""},
		"empty username": {Username: "", Password: "pass"},
		"empty password": {Username: "user", Password: ""},
	}

	for name, creds := range cases {
		t.Run(name, func(t *testing.T) {
			v, err := NewValidator(creds)
			if !errors.Is(err, ErrNoCredentials) {
				t.Fatalf("expected ErrNoCredentials, got %v", err)
			}

			if v.Validate("attacker", "guess") {
				t.Fatal("a validator built from empty credentials authenticated a caller")
			}
		})
	}
}

// TestValidatorValidate checks that only the exact credentials are accepted.
func TestValidatorValidate(t *testing.T) {
	v, err := NewValidator(utils.BasicCredentials{Username: "user", Password: "pass"})
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	cases := []struct {
		name     string
		user     string
		pass     string
		expected bool
	}{
		{name: "correct credentials", user: "user", pass: "pass", expected: true},
		{name: "wrong username", user: "nope", pass: "pass", expected: false},
		{name: "wrong password", user: "user", pass: "nope", expected: false},
		{name: "empty credentials", user: "", pass: "", expected: false},
		{name: "username prefix", user: "use", pass: "pass", expected: false},
		{name: "password prefix", user: "user", pass: "pas", expected: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := v.Validate(tc.user, tc.pass); got != tc.expected {
				t.Fatalf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

// TestNilValidatorDeniesEverything covers the fail-closed default: a server
// that never installed a validator must not serve anybody.
func TestNilValidatorDeniesEverything(t *testing.T) {
	var v *Validator

	if v.Disabled() {
		t.Fatal("a nil validator must not report authentication as disabled")
	}

	if v.Validate("user", "pass") {
		t.Fatal("a nil validator authenticated a caller")
	}
}

// TestDisabledValidator covers the explicit opt-out.
func TestDisabledValidator(t *testing.T) {
	v := NewDisabledValidator()

	if !v.Disabled() {
		t.Fatal("expected the validator to report authentication as disabled")
	}

	if !v.Validate("", "") {
		t.Fatal("a disabled validator must accept credential-less callers")
	}
}

// TestNewFromConfig covers how configuration maps onto validators.
func TestNewFromConfig(t *testing.T) {
	creds := utils.BasicCredentials{Username: "user", Password: "pass"}

	t.Run("default mode requires credentials", func(t *testing.T) {
		if _, err := NewFromConfig(utils.AuthConfig{Mode: ""}, utils.BasicCredentials{}); !errors.Is(
			err, ErrNoCredentials,
		) {
			t.Fatalf("expected ErrNoCredentials, got %v", err)
		}
	})

	t.Run("basic mode requires credentials", func(t *testing.T) {
		if _, err := NewFromConfig(
			utils.AuthConfig{Mode: utils.AuthModeBasic}, utils.BasicCredentials{},
		); !errors.Is(err, ErrNoCredentials) {
			t.Fatalf("expected ErrNoCredentials, got %v", err)
		}
	})

	t.Run("basic mode accepts credentials", func(t *testing.T) {
		v, err := NewFromConfig(utils.AuthConfig{Mode: utils.AuthModeBasic}, creds)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if v.Disabled() || !v.Validate("user", "pass") {
			t.Fatal("expected a working basic validator")
		}
	})

	t.Run("none mode disables authentication", func(t *testing.T) {
		v, err := NewFromConfig(utils.AuthConfig{Mode: " NONE "}, utils.BasicCredentials{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !v.Disabled() {
			t.Fatal("expected authentication to be disabled")
		}
	})

	t.Run("unknown mode is an error", func(t *testing.T) {
		if _, err := NewFromConfig(utils.AuthConfig{Mode: "oauth"}, creds); !errors.Is(
			err, ErrUnknownAuthMode,
		) {
			t.Fatalf("expected ErrUnknownAuthMode, got %v", err)
		}
	})
}

// TestSharedValidator covers the single validator both servers authenticate
// through (SEC-1).
func TestSharedValidator(t *testing.T) {
	previous := Shared()
	t.Cleanup(func() { SetShared(previous) })

	SetShared(nil)

	if Shared().Validate("user", "pass") {
		t.Fatal("an uninstalled shared validator authenticated a caller")
	}

	v, err := NewValidator(utils.BasicCredentials{Username: "user", Password: "pass"})
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	SetShared(v)

	if !Shared().Validate("user", "pass") {
		t.Fatal("the shared validator rejected the configured credentials")
	}
}
