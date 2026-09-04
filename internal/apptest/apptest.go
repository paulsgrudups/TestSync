// Package apptest builds ready-to-use applications for tests.
//
// Every test used to reach into four package globals to install a store,
// credentials, limits and an empty registry, which made the suite
// order-dependent and impossible to run in parallel (TEST-2). A test now asks
// for an application of its own instead, and shares nothing with the test that
// ran before it.
package apptest

import (
	"testing"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/internal/app"
	"github.com/paulsgrudups/testsync/internal/storagetest"
	"github.com/paulsgrudups/testsync/utils"
)

// The credentials every test application accepts.
const (
	Username = "user"
	Password = "pass"
)

// Config returns a configuration with the defaults applied and the test
// credentials set. Callers adjust the fields their test is about.
func Config() utils.Config {
	conf := utils.Config{
		SyncClient: utils.BasicCredentials{Username: Username, Password: Password},
	}

	utils.ApplyDefaults(&conf)

	return conf
}

// Validator returns a validator that accepts [Username] and [Password].
func Validator(t *testing.T) *auth.Validator {
	t.Helper()

	validator, err := auth.NewValidator(
		utils.BasicCredentials{Username: Username, Password: Password},
	)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	return validator
}

// New builds an application from conf, with a data store of its own and a
// validator for the test credentials.
func New(t *testing.T, conf utils.Config) *app.App {
	t.Helper()

	return WithValidator(t, conf, Validator(t))
}

// NewDefault builds an application from the default test configuration.
func NewDefault(t *testing.T) *app.App {
	t.Helper()

	return New(t, Config())
}

// NewInsecure builds an application with authentication deliberately
// disabled, for the tests that are about something other than credentials.
func NewInsecure(t *testing.T) *app.App {
	t.Helper()

	return WithValidator(t, Config(), auth.NewDisabledValidator())
}

// WithValidator builds an application with a caller-supplied validator, for
// the tests that need authentication disabled, or absent.
func WithValidator(
	t *testing.T, conf utils.Config, validator *auth.Validator,
) *app.App {
	t.Helper()

	return app.New(conf, storagetest.NewStore(t), validator)
}
