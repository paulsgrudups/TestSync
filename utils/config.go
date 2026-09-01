// Package utils provides small helper utilities used across the project.
package utils

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

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

	// DefaultHTTPPort is the port the HTTP API listens on when none is set.
	DefaultHTTPPort = 9104

	// DefaultWSPort is the port the WebSocket server listens on when none is
	// set.
	DefaultWSPort = 9105

	// MaxPort is the highest usable TCP port number.
	MaxPort = 65535
)

// Defaults for the janitor that reclaims finished test runs (STAB-3, STAB-5).
const (
	// DefaultCleanupInterval is how often expired runs are swept. The sweep
	// used to run every 12 hours, so up to 12 hours of finished runs were held
	// in memory at once.
	DefaultCleanupInterval = time.Hour

	// DefaultRetention is how long an idle run is kept before it is reclaimed.
	DefaultRetention = 12 * time.Hour
)

// Defaults for the resource limits that bound one server (STAB-3, SEC-8).
// Every one of them is a rejection an operator can raise or lower; none of
// them can be switched off, because an unbounded server is the thing they
// exist to prevent.
const (
	// DefaultMaxTests is how many test runs may be registered at once.
	DefaultMaxTests = 10000

	// DefaultMaxConnectionsPerTest is how many agents may attach to one run.
	DefaultMaxConnectionsPerTest = 256

	// DefaultMaxCheckpointsPerTest is how many distinct checkpoint
	// identifiers one run may create. Barriers are reusable, so a looping
	// suite needs one identifier per barrier, not one per iteration.
	DefaultMaxCheckpointsPerTest = 256

	// DefaultMaxDataBytes bounds a stored payload, an HTTP request body and a
	// single WebSocket frame. It is one number for all three deliberately: a
	// payload that cannot be delivered is of no use to anybody.
	DefaultMaxDataBytes = 10 << 20
)

// Config defines the basic configurable parameters for the service.
type Config struct {
	HTTPPort   int              `json:"http_port"`
	WSPort     int              `json:"ws_port"`
	Logging    LogConfig        `json:"logging"`
	Auth       AuthConfig       `json:"auth"`
	SyncClient BasicCredentials `json:"sync_client"`
	Storage    StorageConfig    `json:"storage"`
	Cleanup    CleanupConfig    `json:"cleanup"`
	Limits     LimitsConfig     `json:"limits"`
}

// CleanupConfig defines how long finished test runs are kept and how often
// they are reclaimed.
type CleanupConfig struct {
	// Interval is how often the janitor sweeps. Defaults to 1h.
	Interval Duration `json:"interval"`

	// Retention is how long a run with no connected agents is kept before it
	// is reclaimed, together with its stored data. Defaults to 12h.
	Retention Duration `json:"retention"`
}

// LimitsConfig bounds the resources one server will hold. Every limit has a
// documented rejection: exceeding one is reported to the client, never
// silently dropped.
type LimitsConfig struct {
	// MaxTests is how many test runs may be registered at once.
	MaxTests int `json:"max_tests"`

	// MaxConnectionsPerTest is how many agents may attach to one run.
	MaxConnectionsPerTest int `json:"max_connections_per_test"`

	// MaxCheckpointsPerTest is how many distinct checkpoint identifiers one
	// run may create.
	MaxCheckpointsPerTest int `json:"max_checkpoints_per_test"`

	// MaxDataBytes bounds a stored payload, an HTTP request body and a single
	// WebSocket frame.
	MaxDataBytes int64 `json:"max_data_bytes"`
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

	if conf.HTTPPort == 0 {
		conf.HTTPPort = DefaultHTTPPort
	}

	if conf.WSPort == 0 {
		conf.WSPort = DefaultWSPort
	}

	if conf.Cleanup.Interval == 0 {
		conf.Cleanup.Interval = Duration(DefaultCleanupInterval)
	}

	if conf.Cleanup.Retention == 0 {
		conf.Cleanup.Retention = Duration(DefaultRetention)
	}

	if conf.Limits.MaxTests == 0 {
		conf.Limits.MaxTests = DefaultMaxTests
	}

	if conf.Limits.MaxConnectionsPerTest == 0 {
		conf.Limits.MaxConnectionsPerTest = DefaultMaxConnectionsPerTest
	}

	if conf.Limits.MaxCheckpointsPerTest == 0 {
		conf.Limits.MaxCheckpointsPerTest = DefaultMaxCheckpointsPerTest
	}

	if conf.Limits.MaxDataBytes == 0 {
		conf.Limits.MaxDataBytes = DefaultMaxDataBytes
	}
}

// Validate reports the first setting the server cannot run with. It is
// checked once at startup so that a mistake in configuration.json is one
// readable sentence rather than a failure hours later (STAB-7).
func Validate(conf *Config) error {
	if conf == nil {
		return errors.New("no configuration")
	}

	if err := validatePort("http_port", conf.HTTPPort); err != nil {
		return err
	}

	if err := validatePort("ws_port", conf.WSPort); err != nil {
		return err
	}

	if conf.HTTPPort == conf.WSPort {
		return fmt.Errorf(
			"http_port and ws_port are both %d; the two servers need different ports",
			conf.HTTPPort,
		)
	}

	limits := map[string]int64{
		"limits.max_tests":                int64(conf.Limits.MaxTests),
		"limits.max_connections_per_test": int64(conf.Limits.MaxConnectionsPerTest),
		"limits.max_checkpoints_per_test": int64(conf.Limits.MaxCheckpointsPerTest),
		"limits.max_data_bytes":           conf.Limits.MaxDataBytes,
	}

	for _, name := range slices.Sorted(maps.Keys(limits)) {
		if limits[name] < 0 {
			return fmt.Errorf(
				"%s is %d; it must be positive, or omitted to use the default",
				name, limits[name],
			)
		}
	}

	return nil
}

// validatePort reports whether a configured port can be listened on.
func validatePort(name string, port int) error {
	if port <= 0 || port > MaxPort {
		return fmt.Errorf(
			"%s is %d; it must be between 1 and %d", name, port, MaxPort,
		)
	}

	return nil
}

// ReadConfig reads file into given config object.
func ReadConfig(filename string, config any) error {
	file, err := afero.ReadFile(FS, filename) //nolint:gosec // reading local config via afero is safe in tests/CI
	if err != nil {
		return err
	}

	return json.Unmarshal(file, config)
}
