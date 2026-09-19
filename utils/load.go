package utils

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/afero"
)

// ConfigFileName is the file [Load] looks for in the configuration directory.
const ConfigFileName = "configuration.json"

// EnvPrefix starts the name of every environment variable the server reads.
const EnvPrefix = "TESTSYNC_"

// DefaultUsername is the username used when only a password is configured, so
// that a container can be started with one secret and nothing else.
const DefaultUsername = "testsync"

// LoadOptions says where [Load] finds each layer of configuration.
type LoadOptions struct {
	// Dir is the directory holding ConfigFileName.
	Dir string

	// RequireFile makes a missing file an error. It is set when the operator
	// named the directory explicitly: a file they pointed at and that is not
	// there is a mistake, while no file in the default place just means
	// "configure me some other way".
	RequireFile bool

	// LookupEnv reads an environment variable. It is [os.LookupEnv] in the
	// server, and a map in tests.
	LookupEnv func(string) (string, bool)

	// Override applies the command-line flags the operator set, the top
	// layer. It may be nil.
	Override func(*Config)
}

// Loaded is a configuration ready to run with, and what the operator should
// be told about how it was assembled.
type Loaded struct {
	Config Config

	// File is the configuration file that was read, or "" when there was
	// none.
	File string

	// Warnings are problems that do not stop the server: an unknown key, a
	// secret in a file anyone can read. They are returned rather than logged
	// because the logger is built from this configuration.
	Warnings []string
}

// Load assembles the configuration from its layers, each overriding the one
// before: defaults, the configuration file, TESTSYNC_* environment variables,
// then flags (API-5, SEC-6). The result is defaulted and validated, so an
// error names the setting and, where it came from the environment, the
// variable.
func Load(opts LoadOptions) (Loaded, error) {
	var loaded Loaded

	filename := filepath.Join(opts.Dir, ConfigFileName)

	found, err := loadFile(filename, &loaded)
	if err != nil {
		return loaded, err
	}

	if !found && opts.RequireFile {
		return loaded, fmt.Errorf(
			"no configuration file at %s: create it, point -c at the directory "+
				"holding it, or configure the server with TESTSYNC_* environment "+
				"variables instead",
			filename,
		)
	}

	if found {
		loaded.File = filename
	}

	if opts.LookupEnv != nil {
		if err := applyEnv(&loaded.Config, opts.LookupEnv); err != nil {
			return loaded, err
		}
	}

	if opts.Override != nil {
		opts.Override(&loaded.Config)
	}

	if err := resolvePasswordFile(&loaded); err != nil {
		return loaded, err
	}

	if loaded.Config.SyncClient.Username == "" && loaded.Config.SyncClient.Password != "" {
		loaded.Config.SyncClient.Username = DefaultUsername
	}

	ApplyDefaults(&loaded.Config)

	if err := Validate(&loaded.Config); err != nil {
		if found {
			return loaded, fmt.Errorf("invalid configuration in %s: %w", filename, err)
		}

		return loaded, fmt.Errorf("invalid configuration: %w", err)
	}

	return loaded, nil
}

// loadFile reads the configuration file into loaded, reporting whether there
// was one. Keys the server does not know are warned about, not refused: a
// typo is worth telling the operator about, but an older server reading a
// newer file should still start.
func loadFile(filename string, loaded *Loaded) (bool, error) {
	body, err := afero.ReadFile(FS, filename)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("could not read %s: %w", filename, err)
	}

	if err := json.Unmarshal(body, &loaded.Config); err != nil {
		return false, fmt.Errorf("could not read %s: %w", filename, err)
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err == nil {
		for _, key := range unknownKeys(reflect.TypeFor[Config](), raw, "") {
			loaded.Warnings = append(loaded.Warnings, fmt.Sprintf(
				"%s: unknown key %q is ignored", filename, key,
			))
		}
	}

	if loaded.Config.SyncClient.Password != "" && PasswordFileSet(loaded.Config.SyncClient) {
		return false, fmt.Errorf(
			"%s sets both sync_client.password and sync_client.password_file; keep one",
			filename,
		)
	}

	if loaded.Config.SyncClient.Password != "" && worldReadable(filename) {
		loaded.Warnings = append(loaded.Warnings, fmt.Sprintf(
			"%s holds the sync_client password and is readable by every user on "+
				"this machine; restrict it with chmod 600, or use "+
				"sync_client.password_file or TESTSYNC_SYNC_CLIENT_PASSWORD",
			filename,
		))
	}

	return true, nil
}

// PasswordFileSet reports whether credentials name a password file.
func PasswordFileSet(c BasicCredentials) bool {
	return strings.TrimSpace(c.PasswordFile) != ""
}

// resolvePasswordFile reads sync_client.password_file into the password. The
// file's trailing newline is dropped - every editor and "echo" adds one - but
// nothing else is, so a password may contain spaces.
//
// Its permissions are not checked, unlike the configuration file's: Docker
// mounts secrets 0444 and Kubernetes 0644 by default, inside a filesystem only
// the container sees, so a warning would fire on every correct deployment.
func resolvePasswordFile(loaded *Loaded) error {
	creds := &loaded.Config.SyncClient
	if !PasswordFileSet(*creds) {
		return nil
	}

	path := strings.TrimSpace(creds.PasswordFile)

	body, err := afero.ReadFile(FS, path)
	if err != nil {
		return fmt.Errorf("could not read sync_client.password_file %s: %w", path, err)
	}

	password := strings.TrimRight(string(body), "\r\n")
	if password == "" {
		return fmt.Errorf("sync_client.password_file %s is empty", path)
	}

	creds.Password = password
	creds.PasswordFile = ""

	return nil
}

// worldReadable reports whether any user may read the file. It is false when
// the file cannot be inspected, which is not this check's failure to report.
func worldReadable(path string) bool {
	info, err := FS.Stat(path)

	return err == nil && info.Mode().Perm()&0o004 != 0
}

// setting is one configuration leaf: where it lives in the file, the
// environment variable that overrides it, and the field to write.
type setting struct {
	key   string // logging.level
	env   string // TESTSYNC_LOGGING_LEVEL
	field reflect.Value
}

// settings lists every leaf of conf. The environment variable names are
// derived from the JSON keys, so a new setting has one without anybody having
// to remember to add it.
func settings(conf *Config) []setting {
	var out []setting

	var walk func(v reflect.Value, prefix string)

	walk = func(v reflect.Value, prefix string) {
		for i := range v.NumField() {
			field := v.Type().Field(i)

			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}

			key := name
			if prefix != "" {
				key = prefix + "." + name
			}

			value := v.Field(i)

			if value.Kind() == reflect.Struct {
				walk(value, key)
				continue
			}

			out = append(out, setting{
				key:   key,
				env:   EnvPrefix + strings.ToUpper(strings.ReplaceAll(key, ".", "_")),
				field: value,
			})
		}
	}

	walk(reflect.ValueOf(conf).Elem(), "")

	return out
}

// EnvVars lists every environment variable the server reads, with the key it
// overrides, for the documentation and for tests.
func EnvVars() map[string]string {
	var conf Config

	vars := make(map[string]string)
	for _, s := range settings(&conf) {
		vars[s.env] = s.key
	}

	return vars
}

// applyEnv overrides conf with every TESTSYNC_* variable that is set. An empty
// variable counts as unset: compose files and CI templates commonly define a
// variable with no value, and that must not wipe a setting from the file.
func applyEnv(conf *Config, lookup func(string) (string, bool)) error {
	for _, s := range settings(conf) {
		raw, ok := lookup(s.env)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}

		if err := setField(s.field, raw); err != nil {
			return fmt.Errorf("%s (%s): %w", s.env, s.key, err)
		}

		// A password set here replaces a password file set in the file, and
		// the other way round: the higher layer wins as a whole.
		switch s.key {
		case "sync_client.password":
			conf.SyncClient.PasswordFile = ""
		case "sync_client.password_file":
			conf.SyncClient.Password = ""
		}
	}

	return nil
}

// setField parses raw into a configuration leaf of any of the kinds Config
// uses.
func setField(field reflect.Value, raw string) error {
	if field.Type() == reflect.TypeFor[Duration]() {
		var d Duration
		if err := d.UnmarshalJSON([]byte(strconv.Quote(strings.TrimSpace(raw)))); err != nil {
			return err
		}

		field.Set(reflect.ValueOf(d))

		return nil
	}

	switch field.Kind() { //nolint:exhaustive // Config uses only these kinds; the rest are refused.
	case reflect.String:
		field.SetString(raw)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return fmt.Errorf("%q is not a whole number", raw)
		}

		field.SetInt(n)
	default:
		return fmt.Errorf("settings of kind %s cannot be set from the environment", field.Kind())
	}

	return nil
}

// unknownKeys lists the keys of raw that t has no field for, as dotted paths.
func unknownKeys(t reflect.Type, raw map[string]any, prefix string) []string {
	known := make(map[string]reflect.Type)

	for i := range t.NumField() {
		field := t.Field(i)

		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			known[name] = field.Type
		}
	}

	var unknown []string

	for key, value := range raw {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}

		fieldType, ok := known[key]
		if !ok {
			unknown = append(unknown, path)
			continue
		}

		if nested, isMap := value.(map[string]any); isMap && fieldType.Kind() == reflect.Struct {
			unknown = append(unknown, unknownKeys(fieldType, nested, path)...)
		}
	}

	slices.Sort(unknown)

	return unknown
}
