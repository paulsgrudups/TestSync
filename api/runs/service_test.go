package runs

import (
	"errors"
	"testing"

	"github.com/paulsgrudups/testsync/internal/storagetest"
)

func TestService_CreateAndRead(t *testing.T) {
	AllTests = make(map[int]*Test)

	service := NewService(storagetest.NewStore(t))
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
	AllTests = make(map[int]*Test)

	service := NewService(storagetest.NewStore(t))
	if err := service.CreateTestData(10, []byte("payload")); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if err := service.CreateTestData(10, []byte("payload")); !errors.Is(err, ErrTestExists) {
		t.Fatalf("expected ErrTestExists, got %v", err)
	}
}

// Services resolve their store once, at construction, so DefaultService must
// be rebound when the store is installed during startup. Without this, the
// init-time DefaultService would hold a nil store for the process lifetime.
func TestSetDataStore_RebindsDefaultService(t *testing.T) {
	originalStore := Store
	originalService := DefaultService
	t.Cleanup(func() {
		Store = originalStore
		DefaultService = originalService
	})

	AllTests = make(map[int]*Test)
	Store = nil
	DefaultService = NewService(nil)

	SetDataStore(storagetest.NewStore(t))

	if err := DefaultService.CreateTestData(1, []byte("payload")); err != nil {
		t.Fatalf("DefaultService was not rebound to the installed store: %v", err)
	}

	data, err := DefaultService.ReadTestData(1)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("unexpected data: %q", string(data))
	}
}
