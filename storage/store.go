package storage

import "time"

// DataStore defines persistence for test data.
type DataStore interface {
	SaveData(testID int, data []byte) error
	LoadData(testID int) ([]byte, bool, error)
	DeleteData(testID int) error
	DeleteOlderThan(limit time.Time) error
	Close() error
}
