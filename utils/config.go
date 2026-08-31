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
)

// Config defines the basic configurable parameters for the service.
type Config struct {
	HTTPPort   int              `json:"http_port"`
	WSPort     int              `json:"ws_port"`
	Logging    LogConfig        `json:"logging"`
	SyncClient BasicCredentials `json:"sync_client"`
	Storage    StorageConfig    `json:"storage"`
}

// BasicCredentials defines generic client details.
type BasicCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
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
func ReadConfig(filename string, config interface{}) error {
	file, err := afero.ReadFile(FS, filename) // nolint: gosec
	if err != nil {
		return err
	}

	return json.Unmarshal(file, &config)
}
