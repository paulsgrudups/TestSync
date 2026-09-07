package runs

import (
	"errors"
	"testing"
	"time"

	"github.com/paulsgrudups/testsync/internal/storagetest"
)

func TestService_CreateAndRead(t *testing.T) {
	t.Parallel()

	_, service := newTestSetup(t, DefaultLimits())

	if err := service.CreateTestData(t.Context(), 10, []byte("payload")); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	data, err := service.ReadTestData(t.Context(), 10)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	if string(data) != "payload" {
		t.Fatalf("unexpected data: %q", string(data))
	}
}

func TestService_CreateDuplicate(t *testing.T) {
	t.Parallel()

	_, service := newTestSetup(t, DefaultLimits())

	if err := service.CreateTestData(t.Context(), 10, []byte("payload")); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if err := service.CreateTestData(t.Context(), 10, []byte("payload")); !errors.Is(err, ErrTestExists) {
		t.Fatalf("expected ErrTestExists, got %v", err)
	}
}

// TestService_WithoutStore covers the store guard. A service resolves its
// store once, at construction, so one built without a store can never acquire
// one: it reports the misconfiguration rather than dereferencing nil in a
// request goroutine.
func TestService_WithoutStore(t *testing.T) {
	t.Parallel()

	service := NewService(nil, NewRegistry(DefaultLimits(), nil), nil)

	if err := service.CreateTestData(t.Context(), 1, []byte("payload")); !errors.Is(err, ErrNoDataStore) {
		t.Fatalf("expected ErrNoDataStore, got %v", err)
	}

	if _, err := service.ReadTestData(t.Context(), 1); !errors.Is(err, ErrNoDataStore) {
		t.Fatalf("expected ErrNoDataStore, got %v", err)
	}
}

// TestService_UsesItsOwnRegistry is the other half of CODE-1: two services in
// one process must not see each other's runs.
func TestService_UsesItsOwnRegistry(t *testing.T) {
	t.Parallel()

	first := NewRegistry(DefaultLimits(), nil)
	second := NewRegistry(DefaultLimits(), nil)

	firstService := NewService(storagetest.NewStore(t), first, nil)
	secondService := NewService(storagetest.NewStore(t), second, nil)

	if err := firstService.CreateTestData(t.Context(), 7, []byte("one")); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if _, err := secondService.ReadTestData(t.Context(), 7); !errors.Is(err, ErrTestNotFound) {
		t.Fatalf("expected ErrTestNotFound from the second service, got %v", err)
	}

	if _, ok := second.Get(7); ok {
		t.Fatal("a run created through one service appeared in another registry")
	}
}

// TestReadTestDataHasOneSource is the CODE-3 regression test.
//
// A payload lived in the store and in a copy cached on the run, and
// ReadTestData consulted the second when the first had no row. The two were
// written and reclaimed independently, so once the janitor had deleted a
// stored row the server went on serving the stale in-memory copy of it.
func TestReadTestDataHasOneSource(t *testing.T) {
	t.Parallel()

	registry, service := newTestSetup(t, DefaultLimits())

	if err := service.CreateTestData(t.Context(), 1, []byte("payload")); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Reclaim the stored row while leaving the run registered, which is the
	// state a sweep leaves behind between its two steps.
	if err := service.DeleteDataOlderThan(t.Context(), time.Now().Add(time.Hour), nil); err != nil {
		t.Fatalf("failed to delete stored data: %v", err)
	}

	if _, ok := registry.Get(1); !ok {
		t.Fatal("the run under test was unregistered, so this proves nothing")
	}

	if _, err := service.ReadTestData(t.Context(), 1); !errors.Is(err, ErrTestNotFound) {
		t.Fatalf(
			"a payload deleted from the store was still served: got %v", err,
		)
	}
}
