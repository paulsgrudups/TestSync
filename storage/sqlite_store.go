package storage

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore persists test data in sqlite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore initializes sqlite store at given path.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS test_data (
			test_id INTEGER PRIMARY KEY,
			data BLOB,
			created_at INTEGER NOT NULL
		)
	`); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &SQLiteStore{db: db}, nil
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
