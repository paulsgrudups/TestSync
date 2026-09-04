package runs

import (
	"errors"
	"testing"

	"github.com/paulsgrudups/testsync/internal/storagetest"
)

func TestService_CreateAndRead(t *testing.T) {
	t.Parallel()

	_, service := newTestSetup(t, DefaultLimits())

	if err := service.CreateTestData(10, []byte("payload")); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	data, err := service.ReadTestData(10)
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

	if err := service.CreateTestData(10, []byte("payload")); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if err := service.CreateTestData(10, []byte("payload")); !errors.Is(err, ErrTestExists) {
		t.Fatalf("expected ErrTestExists, got %v", err)
	}
}

// TestService_WithoutStore covers the store guard. A service resolves its
// store once, at construction, so one built without a store can never acquire
// one: it reports the misconfiguration rather than dereferencing nil in a
// request goroutine.
func TestService_WithoutStore(t *testing.T) {
	t.Parallel()

	service := NewService(nil, NewRegistry(DefaultLimits()))

	if err := service.CreateTestData(1, []byte("payload")); !errors.Is(err, ErrNoDataStore) {
		t.Fatalf("expected ErrNoDataStore, got %v", err)
	}

	if _, err := service.ReadTestData(1); !errors.Is(err, ErrNoDataStore) {
		t.Fatalf("expected ErrNoDataStore, got %v", err)
	}
}

// TestService_UsesItsOwnRegistry is the other half of CODE-1: two services in
// one process must not see each other's runs.
func TestService_UsesItsOwnRegistry(t *testing.T) {
	t.Parallel()

	first := NewRegistry(DefaultLimits())
	second := NewRegistry(DefaultLimits())

	firstService := NewService(storagetest.NewStore(t), first)
	secondService := NewService(storagetest.NewStore(t), second)

	if err := firstService.CreateTestData(7, []byte("one")); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if _, err := secondService.ReadTestData(7); !errors.Is(err, ErrTestNotFound) {
		t.Fatalf("expected ErrTestNotFound from the second service, got %v", err)
	}

	if _, ok := second.Get(7); ok {
		t.Fatal("a run created through one service appeared in another registry")
	}
}
