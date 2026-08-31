// Package storagetest provides helpers for tests that need a data store.
package storagetest

import (
	"path/filepath"
	"testing"

	"github.com/paulsgrudups/testsync/storage"
)

// NewStore returns a sqlite store backed by a database in the test's temporary
// directory. The store is closed when the test finishes.
func NewStore(t *testing.T) *storage.SQLiteStore {
	t.Helper()

	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "testsync.db"))
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return store
}
