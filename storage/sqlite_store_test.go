package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteStore_SaveLoadDelete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "testsync.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.SaveData(1, []byte("data")); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	data, ok, err := store.LoadData(1)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if !ok || string(data) != "data" {
		t.Fatalf("unexpected load result: ok=%v data=%q", ok, string(data))
	}

	if err := store.DeleteData(1); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	_, ok, err = store.LoadData(1)
	if err != nil {
		t.Fatalf("load after delete failed: %v", err)
	}
	if ok {
		t.Fatal("expected no data after delete")
	}
}

func TestSQLiteStore_DeleteOlderThan(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "testsync.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.SaveData(1, []byte("data")); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	if err := store.DeleteOlderThan(time.Now().Add(1 * time.Hour)); err != nil {
		t.Fatalf("delete older than failed: %v", err)
	}

	_, ok, err := store.LoadData(1)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if ok {
		t.Fatal("expected data to be deleted")
	}
}

func TestSQLiteStore_CreatesDatabaseAndParentDirs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "dir", "testsync.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected database file to be created at %q: %v", dbPath, err)
	}
}

func TestSQLiteStore_PersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "testsync.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	if err := store.SaveData(7, []byte("persisted")); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	reopened, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	data, ok, err := reopened.LoadData(7)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if !ok {
		t.Fatal("expected data to survive reopen")
	}
	if string(data) != "persisted" {
		t.Fatalf("unexpected data: %q", string(data))
	}
}

func TestSQLiteStore_RecreatesUnusableDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "testsync.db")

	if err := os.WriteFile(dbPath, []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatalf("failed to write corrupt file: %v", err)
	}

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("expected corrupt database to be recreated, got error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.SaveData(1, []byte("fresh")); err != nil {
		t.Fatalf("save failed on recreated database: %v", err)
	}

	data, ok, err := store.LoadData(1)
	if err != nil || !ok || string(data) != "fresh" {
		t.Fatalf("unexpected read from recreated database: %q ok=%v err=%v", string(data), ok, err)
	}

	matches, err := filepath.Glob(dbPath + ".corrupt-*")
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected the unusable database to be preserved, found %d backups", len(matches))
	}
}

func TestSQLiteStore_PreservesUnreadableDatabase(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}

	dbPath := filepath.Join(t.TempDir(), "testsync.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	if err := store.SaveData(1, []byte("precious")); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	// A database we cannot open is not a database we may discard: only
	// genuine corruption justifies replacing the file.
	if err := os.Chmod(dbPath, 0o000); err != nil {
		t.Fatalf("chmod failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dbPath, 0o600) })

	if _, err := NewSQLiteStore(dbPath); err == nil {
		t.Fatal("expected an unreadable database to fail rather than be recreated")
	}

	matches, err := filepath.Glob(dbPath + ".corrupt-*")
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("a healthy but unreadable database was moved aside (%d backups)", len(matches))
	}

	if err := os.Chmod(dbPath, 0o600); err != nil {
		t.Fatalf("chmod failed: %v", err)
	}

	reopened, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen restored database: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	data, ok, err := reopened.LoadData(1)
	if err != nil || !ok || string(data) != "precious" {
		t.Fatalf("data did not survive: %q ok=%v err=%v", string(data), ok, err)
	}
}

func TestSQLiteStore_HandlesPathsWithURIMetacharacters(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"we#ird.db", "qu?ery.db", "pct%20.db", "with space.db"} {
		t.Run(name, func(t *testing.T) {
			dbPath := filepath.Join(dir, name)

			store, err := NewSQLiteStore(dbPath)
			if err != nil {
				t.Fatalf("failed to create sqlite store: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })

			if err := store.SaveData(1, []byte("data")); err != nil {
				t.Fatalf("save failed: %v", err)
			}

			// The driver parses the DSN as a URI, so an unescaped path would
			// quietly create the database somewhere else.
			if _, err := os.Stat(dbPath); err != nil {
				t.Fatalf("database was not created at the configured path %q: %v", dbPath, err)
			}
		})
	}
}

func TestSQLiteStore_LeavesNoStaleSidecarsAfterRecreate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "testsync.db")

	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(dbPath+suffix, []byte("not a database"), 0o600); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}
	}

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("expected corrupt database to be recreated: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// The corrupt file itself is kept for debugging.
	matches, err := filepath.Glob(dbPath + ".corrupt-*")
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("the corrupt database was not preserved")
	}

	// Sidecars belonging to the discarded database must not be left next to
	// the new one, where they would be read as its state. SQLite removes them
	// itself while failing to open, but the recreate path must not restore
	// them either.
	for _, suffix := range []string{"-wal", "-shm"} {
		data, err := os.ReadFile(dbPath + suffix) //nolint:gosec
		if err != nil {
			continue // absent is fine
		}
		if string(data) == "not a database" {
			t.Fatalf("stale %q sidecar from the discarded database survived", suffix)
		}
	}

	if err := store.SaveData(1, []byte("fresh")); err != nil {
		t.Fatalf("save failed on recreated database: %v", err)
	}
}
