// Package storage implements data storage backends used by TestSync.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

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
//
// The logger is used for exactly one message, the one that says a database was
// moved aside. It is a parameter rather than the slog default because an
// operator who lost a database must find that line in the same place, at the
// same level and in the same format as everything else the server logged. A
// nil logger discards.
func NewSQLiteStore(
	ctx context.Context, path string, logger *slog.Logger,
) (*SQLiteStore, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("could not create sqlite directory %q: %w", dir, err)
		}
	}

	store, err := openSQLite(ctx, path)
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
	logger.WarnContext(ctx,
		"existing sqlite database is corrupt; moving it aside and creating a new one",
		"path", path,
		"backup", backup,
		"error", err,
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

	return openSQLite(ctx, path)
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
func openSQLite(ctx context.Context, path string) (*SQLiteStore, error) {
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

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("could not reach sqlite database %q: %w", path, err)
	}

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS test_data (
			test_id INTEGER PRIMARY KEY,
			data BLOB,
			created_at INTEGER NOT NULL
		)
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("could not create schema in sqlite database %q: %w", path, err)
	}

	// The janitor's only query selects by age, and without this it was a full
	// table scan on every sweep: at ten thousand rows, the configured default
	// maximum, that is 3.5 ms of scanning against 18 µs of indexed search.
	// IF NOT EXISTS so that a database written by an older build gains the
	// index the first time this one opens it.
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_test_data_created_at
			ON test_data(created_at)
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("could not create index in sqlite database %q: %w", path, err)
	}

	return &SQLiteStore{db: db, path: path}, nil
}

// Path returns the file backing the store.
func (s *SQLiteStore) Path() string {
	return s.path
}

// SaveData stores or updates the blob for the given test ID.
func (s *SQLiteStore) SaveData(ctx context.Context, testID int, data []byte) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO test_data (test_id, data, created_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(test_id) DO UPDATE SET data=excluded.data, created_at=excluded.created_at`,
		testID,
		data,
		time.Now().UnixMilli(),
	)
	return err
}

// LoadData returns stored data for a test ID. The boolean indicates whether
// the row existed.
func (s *SQLiteStore) LoadData(ctx context.Context, testID int) ([]byte, bool, error) {
	row := s.db.QueryRowContext(
		ctx, `SELECT data FROM test_data WHERE test_id = ?`, testID,
	)
	var data []byte
	if err := row.Scan(&data); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}

	return data, true, nil
}

// DataSize returns the size in bytes of the stored payload for a test ID. The
// boolean indicates whether the row existed. length() is answered from the
// blob header, so the payload itself is never read.
func (s *SQLiteStore) DataSize(ctx context.Context, testID int) (int, bool, error) {
	row := s.db.QueryRowContext(
		ctx, `SELECT length(data) FROM test_data WHERE test_id = ?`, testID,
	)

	var size sql.NullInt64
	if err := row.Scan(&size); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}

		return 0, false, err
	}

	return int(size.Int64), true, nil
}

// DataSizes returns the size of every stored payload, keyed by test ID.
func (s *SQLiteStore) DataSizes(ctx context.Context) (map[int]int, error) {
	rows, err := s.db.QueryContext(
		ctx, `SELECT test_id, length(data) FROM test_data`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	sizes := make(map[int]int)

	for rows.Next() {
		var (
			testID int
			size   sql.NullInt64
		)

		if err := rows.Scan(&testID, &size); err != nil {
			return nil, err
		}

		sizes[testID] = int(size.Int64)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return sizes, nil
}

// DeleteData removes data for the given test ID.
func (s *SQLiteStore) DeleteData(ctx context.Context, testID int) error {
	_, err := s.db.ExecContext(
		ctx, `DELETE FROM test_data WHERE test_id = ?`, testID,
	)
	return err
}

// DeleteOlderThanExcept deletes rows older than the provided limit, leaving
// the rows named in keepIDs in place whatever their age. The caller uses that
// to protect runs whose agents are still connected.
//
// keepIDs is bounded by the configured maximum number of runs, which is far
// below sqlite's limit on bound parameters, and is empty on the common path.
func (s *SQLiteStore) DeleteOlderThanExcept(
	ctx context.Context, limit time.Time, keepIDs []int,
) error {
	args := make([]any, 0, len(keepIDs)+1)
	args = append(args, limit.UnixMilli())

	query := `DELETE FROM test_data WHERE created_at < ?`

	if len(keepIDs) > 0 {
		placeholders := strings.Repeat(",?", len(keepIDs))[1:]
		//nolint:gosec // The appended fragment is only "?" placeholders; every
		// keepID is passed as a bound parameter below, never interpolated.
		query += ` AND test_id NOT IN (` + placeholders + `)`

		for _, id := range keepIDs {
			args = append(args, id)
		}
	}

	_, err := s.db.ExecContext(ctx, query, args...)

	return err
}

// Close closes the underlying database.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
