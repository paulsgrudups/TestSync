package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSQLiteStore_SaveLoadDelete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "testsync.db")
	store, err := NewSQLiteStore(t.Context(), dbPath, nil)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err = store.SaveData(t.Context(), 1, []byte("data")); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	data, ok, err := store.LoadData(t.Context(), 1)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if !ok || string(data) != "data" {
		t.Fatalf("unexpected load result: ok=%v data=%q", ok, string(data))
	}

	if err = store.DeleteData(t.Context(), 1); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	_, ok, err = store.LoadData(t.Context(), 1)
	if err != nil {
		t.Fatalf("load after delete failed: %v", err)
	}
	if ok {
		t.Fatal("expected no data after delete")
	}
}

func TestSQLiteStore_DeleteOlderThan(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "testsync.db")
	store, err := NewSQLiteStore(t.Context(), dbPath, nil)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err = store.SaveData(t.Context(), 1, []byte("data")); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	if err = store.DeleteOlderThanExcept(t.Context(), time.Now().Add(1*time.Hour), nil); err != nil {
		t.Fatalf("delete older than failed: %v", err)
	}

	_, ok, err := store.LoadData(t.Context(), 1)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if ok {
		t.Fatal("expected data to be deleted")
	}
}

func TestSQLiteStore_CreatesDatabaseAndParentDirs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "dir", "testsync.db")

	store, err := NewSQLiteStore(t.Context(), dbPath, nil)
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

	store, err := NewSQLiteStore(t.Context(), dbPath, nil)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	if err = store.SaveData(t.Context(), 7, []byte("persisted")); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	reopened, err := NewSQLiteStore(t.Context(), dbPath, nil)
	if err != nil {
		t.Fatalf("failed to reopen sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	data, ok, err := reopened.LoadData(t.Context(), 7)
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

	store, err := NewSQLiteStore(t.Context(), dbPath, nil)
	if err != nil {
		t.Fatalf("expected corrupt database to be recreated, got error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err = store.SaveData(t.Context(), 1, []byte("fresh")); err != nil {
		t.Fatalf("save failed on recreated database: %v", err)
	}

	data, ok, err := store.LoadData(t.Context(), 1)
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

	store, err := NewSQLiteStore(t.Context(), dbPath, nil)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	if err = store.SaveData(t.Context(), 1, []byte("precious")); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	// A database we cannot open is not a database we may discard: only
	// genuine corruption justifies replacing the file.
	if err = os.Chmod(dbPath, 0o000); err != nil {
		t.Fatalf("chmod failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dbPath, 0o600) })

	if _, err = NewSQLiteStore(t.Context(), dbPath, nil); err == nil {
		t.Fatal("expected an unreadable database to fail rather than be recreated")
	}

	matches, err := filepath.Glob(dbPath + ".corrupt-*")
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("a healthy but unreadable database was moved aside (%d backups)", len(matches))
	}

	if err = os.Chmod(dbPath, 0o600); err != nil {
		t.Fatalf("chmod failed: %v", err)
	}

	reopened, err := NewSQLiteStore(t.Context(), dbPath, nil)
	if err != nil {
		t.Fatalf("failed to reopen restored database: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	data, ok, err := reopened.LoadData(t.Context(), 1)
	if err != nil || !ok || string(data) != "precious" {
		t.Fatalf("data did not survive: %q ok=%v err=%v", string(data), ok, err)
	}
}

func TestSQLiteStore_HandlesPathsWithURIMetacharacters(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"we#ird.db", "qu?ery.db", "pct%20.db", "with space.db"} {
		t.Run(name, func(t *testing.T) {
			dbPath := filepath.Join(dir, name)

			store, err := NewSQLiteStore(t.Context(), dbPath, nil)
			if err != nil {
				t.Fatalf("failed to create sqlite store: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })

			if err := store.SaveData(t.Context(), 1, []byte("data")); err != nil {
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

	store, err := NewSQLiteStore(t.Context(), dbPath, nil)
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

	if err := store.SaveData(t.Context(), 1, []byte("fresh")); err != nil {
		t.Fatalf("save failed on recreated database: %v", err)
	}
}

// TestSweepUsesTheCreatedAtIndex covers STAB-9's remaining half. The janitor's
// only query selects by age, and it used to be a full table scan on every
// sweep: at the configured default of ten thousand runs that measured 3.5 ms
// against 18 µs once the index existed.
func TestSweepUsesTheCreatedAtIndex(t *testing.T) {
	store, err := NewSQLiteStore(t.Context(), filepath.Join(t.TempDir(), "indexed.db"), nil)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	t.Cleanup(func() { _ = store.Close() })

	var plan string

	row := store.db.QueryRowContext(t.Context(),
		`EXPLAIN QUERY PLAN DELETE FROM test_data WHERE created_at < ?`,
		time.Now().UnixMilli(),
	)

	var id, parent, notUsed int
	if err := row.Scan(&id, &parent, &notUsed, &plan); err != nil {
		t.Fatalf("failed to read the query plan: %v", err)
	}

	if !strings.Contains(plan, "idx_test_data_created_at") {
		t.Fatalf("the sweep does not use the created_at index: %q", plan)
	}
}

// TestIndexIsAddedToAnExistingDatabase covers the upgrade path: a database
// written by a build without the index gains it when this build opens it.
func TestIndexIsAddedToAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")

	store, err := NewSQLiteStore(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	if err = store.SaveData(t.Context(), 1, []byte("payload")); err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	if _, err = store.db.ExecContext(t.Context(), `DROP INDEX idx_test_data_created_at`); err != nil {
		t.Fatalf("failed to drop the index: %v", err)
	}

	if err = store.Close(); err != nil {
		t.Fatalf("failed to close: %v", err)
	}

	reopened, err := NewSQLiteStore(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("failed to reopen: %v", err)
	}

	t.Cleanup(func() { _ = reopened.Close() })

	var name string

	err = reopened.db.QueryRowContext(t.Context(),
		`SELECT name FROM sqlite_master
		 WHERE type = 'index' AND name = 'idx_test_data_created_at'`,
	).Scan(&name)
	if err != nil {
		t.Fatalf("the index was not restored on reopen: %v", err)
	}

	// The reopen must not have cost the existing data.
	if _, ok, err := reopened.LoadData(t.Context(), 1); err != nil || !ok {
		t.Fatalf("data did not survive the reopen: ok=%v err=%v", ok, err)
	}
}

// TestConcurrentSavesSucceed is STAB-9's done-condition, and it guards the
// busy_timeout pragma specifically. SQLite admits one writer at a time; with
// the pragma removed, 42 of these 50 saves fail outright with SQLITE_BUSY
// rather than waiting their turn.
func TestConcurrentSavesSucceed(t *testing.T) {
	const savers = 50

	store, err := NewSQLiteStore(t.Context(), filepath.Join(t.TempDir(), "concurrent.db"), nil)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	t.Cleanup(func() { _ = store.Close() })

	var wg sync.WaitGroup

	failures := make(chan error, savers)

	// Released together, so the writers genuinely contend for the write lock
	// instead of arriving one after another.
	start := make(chan struct{})

	for id := range savers {
		wg.Go(func() {
			<-start

			if err := store.SaveData(t.Context(), id, make([]byte, 8192)); err != nil {
				failures <- err
			}
		})
	}

	close(start)
	wg.Wait()
	close(failures)

	count := 0

	for err := range failures {
		count++

		if count == 1 {
			t.Errorf("a concurrent save failed: %v", err)
		}
	}

	if count > 0 {
		t.Fatalf("%d of %d concurrent saves failed", count, savers)
	}

	// Every payload is readable afterwards, so the writes were not merely
	// accepted and lost.
	for id := range savers {
		if _, ok, err := store.LoadData(t.Context(), id); err != nil || !ok {
			t.Fatalf("payload %d did not survive: ok=%v err=%v", id, ok, err)
		}
	}
}

// TestCancelledContextStopsAQuery covers STAB-10. Every store method used to
// take no context, so a query outlived the request that asked for it: a client
// that gave up, or a request that hit the server's own timeout, left work
// running against the database with nobody waiting for the answer.
func TestCancelledContextStopsAQuery(t *testing.T) {
	store, err := NewSQLiteStore(t.Context(), filepath.Join(t.TempDir(), "ctx.db"), nil)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	t.Cleanup(func() { _ = store.Close() })

	if err = store.SaveData(t.Context(), 1, []byte("payload")); err != nil {
		t.Fatalf("failed to seed: %v", err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	if _, _, err = store.LoadData(cancelled, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected a cancelled read to fail with context.Canceled, got %v", err)
	}

	if err = store.SaveData(cancelled, 2, []byte("payload")); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected a cancelled write to fail with context.Canceled, got %v", err)
	}

	if _, err = store.DataSizes(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected a cancelled listing to fail with context.Canceled, got %v", err)
	}

	// The store is unharmed: a cancelled caller must not break it for anyone
	// else, and the write it abandoned must not have landed.
	if _, ok, err := store.LoadData(t.Context(), 1); err != nil || !ok {
		t.Fatalf("the store stopped working after a cancelled query: ok=%v err=%v", ok, err)
	}

	if _, ok, err := store.LoadData(t.Context(), 2); err != nil || ok {
		t.Fatalf("a cancelled write was stored anyway: ok=%v err=%v", ok, err)
	}
}
