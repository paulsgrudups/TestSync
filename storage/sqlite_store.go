package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	log "github.com/sirupsen/logrus"
	sqlite "modernc.org/sqlite"
)

// SQLite result codes that mean the file is not a usable database. Any other
// failure (permissions, disk space, file descriptor limits) says nothing about
// the file's contents and must never cause it to be discarded.
const (
	sqliteCorrupt = 11 // SQLITE_CORRUPT
	sqliteNotADB  = 26 // SQLITE_NOTADB
)

// SQLiteStore persists test data in sqlite.
type SQLiteStore struct {
	db   *sql.DB
	path string
}

// NewSQLiteStore opens the sqlite database at path, creating it when it does
// not exist. Missing parent directories are created. If an existing file is
// present but unusable as a database, it is moved aside and a fresh database
// is created in its place so that a corrupted file never blocks startup.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("could not create sqlite directory %q: %w", dir, err)
		}
	}

	store, err := openSQLite(path)
	if err == nil {
		return store, nil
	}

	// Only a genuinely corrupt file is replaced. Every other failure is
	// reported as-is, so that a permissions mistake or a full disk never
	// destroys a healthy database.
	if !isCorruptErr(err) {
		return nil, err
	}

	if _, statErr := os.Stat(path); statErr != nil {
		return nil, err
	}

	backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixMilli())
	log.Warnf(
		"Existing sqlite database %q is corrupt (%s); moving it to %q and creating a new one",
		path, err, backup,
	)

	// Move the sidecar files alongside the database. The write-ahead log can
	// hold the most recent transactions, so discarding it would make the
	// preserved copy useless for recovery.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, statErr := os.Stat(path + suffix); statErr != nil {
			continue
		}

		if renameErr := os.Rename(path+suffix, backup+suffix); renameErr != nil {
			return nil, fmt.Errorf(
				"could not move corrupt sqlite database %q aside: %w", path+suffix, renameErr,
			)
		}
	}

	return openSQLite(path)
}

// isCorruptErr reports whether err means the file is not a usable database.
func isCorruptErr(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}

	// The driver reports extended result codes; the low 8 bits hold the
	// primary code.
	switch sqliteErr.Code() & 0xff {
	case sqliteCorrupt, sqliteNotADB:
		return true
	default:
		return false
	}
}

// openSQLite opens path and ensures the schema is present.
func openSQLite(path string) (*SQLiteStore, error) {
	// WAL keeps readers from blocking the writer, and a busy timeout lets
	// concurrent agents wait for the write lock instead of failing outright.
	// The path is escaped because the driver parses the DSN as a URI, where
	// an unescaped "#", "?" or "%" would silently truncate or rewrite it.
	query := url.Values{}
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "synchronous(NORMAL)")

	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + query.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("could not open sqlite database %q: %w", path, err)
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("could not reach sqlite database %q: %w", path, err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS test_data (
			test_id INTEGER PRIMARY KEY,
			data BLOB,
			created_at INTEGER NOT NULL
		)
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("could not create schema in sqlite database %q: %w", path, err)
	}

	return &SQLiteStore{db: db, path: path}, nil
}

// Path returns the file backing the store.
func (s *SQLiteStore) Path() string {
	return s.path
}

func (s *SQLiteStore) SaveData(testID int, data []byte) error {
	_, err := s.db.Exec(
		`INSERT INTO test_data (test_id, data, created_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(test_id) DO UPDATE SET data=excluded.data, created_at=excluded.created_at`,
		testID,
		data,
		time.Now().UnixMilli(),
	)
	return err
}

func (s *SQLiteStore) LoadData(testID int) ([]byte, bool, error) {
	row := s.db.QueryRow(`SELECT data FROM test_data WHERE test_id = ?`, testID)
	var data []byte
	if err := row.Scan(&data); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}

	return data, true, nil
}

func (s *SQLiteStore) DeleteData(testID int) error {
	_, err := s.db.Exec(`DELETE FROM test_data WHERE test_id = ?`, testID)
	return err
}

func (s *SQLiteStore) DeleteOlderThan(limit time.Time) error {
	_, err := s.db.Exec(`DELETE FROM test_data WHERE created_at < ?`, limit.UnixMilli())
	return err
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
