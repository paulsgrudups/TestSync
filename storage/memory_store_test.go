package storage

import (
	"testing"
	"time"
)

func TestMemoryStore_SaveLoadDelete(t *testing.T) {
	store := NewMemoryStore()

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

func TestMemoryStore_DeleteOlderThan(t *testing.T) {
	store := NewMemoryStore()

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
