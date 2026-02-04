package runs

import (
	"testing"

	"github.com/paulsgrudups/testsync/storage"
)

func TestService_CreateAndRead(t *testing.T) {
	AllTests = make(map[int]*Test)

	service := NewService(storage.NewMemoryStore())
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

	service := NewService(storage.NewMemoryStore())
	if err := service.CreateTestData(10, []byte("payload")); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if err := service.CreateTestData(10, []byte("payload")); err != ErrTestExists {
		t.Fatalf("expected ErrTestExists, got %v", err)
	}
}
