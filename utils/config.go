// Package utils provides small helper utilities used across the project.
package utils

import (
	"encoding/json"

	"github.com/spf13/afero"
)

// FS holds implementation of functions provided by os package.
var FS = afero.NewOsFs()

const (
	// StorageTypeSQLite is the only supported storage backend.
	StorageTypeSQLite = "sqlite"

	// DefaultSQLitePath is the database path used when none is configured.
	DefaultSQLitePath = "./testsync.db"

	// AuthModeBasic requires HTTP Basic credentials on every request. It is
	// the default and the only secure mode.
	AuthModeBasic = "basic"

	// AuthModeNone disables authentication entirely. It is an explicit
	// opt-out for local development and is announced with a warning banner on
	// every startup.
	AuthModeNone = "none"
)

// Config defines the basic configurable parameters for the service.
type Config struct {
	HTTPPort   int              `json:"http_port"`
	WSPort     int              `json:"ws_port"`
	Logging    LogConfig        `json:"logging"`
	Auth       AuthConfig       `json:"auth"`
	SyncClient BasicCredentials `json:"sync_client"`
	Storage    StorageConfig    `json:"storage"`
}

// BasicCredentials defines generic client details.
type BasicCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// AuthConfig defines how incoming requests are authenticated.
type AuthConfig struct {
	// Mode selects the authentication mode. "basic" (the default) requires the
	// sync_client credentials on every request; "none" disables authentication
	// entirely and must only be used on a trusted development machine.
	Mode string `json:"mode"`
}

// LogConfig defines configuration variables for logging settings.
type LogConfig struct {
	// Which log level to use.
	// Available values: DEBUG, INFO, WARN, ERROR.
	// defautls to INFO.
	Level string `json:"level"`

	// Directory where to save log file.
	Dir string `json:"dir"`
}

// StorageConfig defines storage settings for test data.
type StorageConfig struct {
	// Type is retained for backwards compatibility with older configuration
	// files. The only supported value is "sqlite"; anything else is ignored
	// with a warning at startup.
	Type string `json:"type"`

	// SQLitePath defines the sqlite db path. The database is created if it
	// does not already exist. Defaults to "./testsync.db".
	SQLitePath string `json:"sqlite_path"`
}

// ApplyDefaults fills in default values for missing config fields.
func ApplyDefaults(conf *Config) {
	if conf == nil {
		return
	}

	if conf.Auth.Mode == "" {
		conf.Auth.Mode = AuthModeBasic
	}

	if conf.Logging.Level == "" {
		conf.Logging.Level = "INFO"
	}

	if conf.Logging.Dir == "" {
		conf.Logging.Dir = "."
	}

	if conf.Storage.SQLitePath == "" {
		conf.Storage.SQLitePath = DefaultSQLitePath
	}
}

// ReadConfig reads file into given config object.
func ReadConfig(filename string, config any) error {
	file, err := afero.ReadFile(FS, filename) //nolint:gosec // reading local config via afero is safe in tests/CI
	if err != nil {
		return err
	}

	return json.Unmarshal(file, config)
}
