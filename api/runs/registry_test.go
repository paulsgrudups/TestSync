package runs

import (
	"errors"
	"testing"
	"time"
)

func TestRegistryEnsureAndGet(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(DefaultLimits())

	created, err := registry.Ensure(10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if created == nil {
		t.Fatal("expected created test, got nil")
	}

	second, err := registry.Ensure(10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if created != second {
		t.Fatal("expected Ensure to return the existing test")
	}

	got, ok := registry.Get(10)
	if !ok || got != created {
		t.Fatal("expected Get to return the created test")
	}
}

func TestRegistryDelete(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(DefaultLimits())

	if _, err := registry.Ensure(5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := registry.Get(5); !ok {
		t.Fatal("expected test to exist before delete")
	}

	registry.Delete(5)

	if _, ok := registry.Get(5); ok {
		t.Fatal("expected test to be deleted")
	}
}

// TestRegistriesAreIndependent is the CODE-1 regression test: the runs of one
// server were held in an exported package-level map, so two servers could not
// exist in one process and every test that touched it affected the next.
func TestRegistriesAreIndependent(t *testing.T) {
	t.Parallel()

	first := NewRegistry(DefaultLimits())
	second := NewRegistry(DefaultLimits())

	if _, err := first.Ensure(1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := second.Get(1); ok {
		t.Fatal("a run registered in one registry appeared in another")
	}

	if count := second.Count(); count != 0 {
		t.Fatalf("expected the second registry to be empty, got %d runs", count)
	}
}

// TestRegistryLimitsAreFixedAtConstruction covers the replacement for the
// limits global: a run carries the limits of the registry that created it, so
// a second server with different limits cannot change what the first enforces.
func TestRegistryLimitsAreFixedAtConstruction(t *testing.T) {
	t.Parallel()

	limits := DefaultLimits()
	limits.MaxTests = 1

	strict := NewRegistry(limits)
	relaxed := NewRegistry(DefaultLimits())

	if _, err := strict.Ensure(1); err != nil {
		t.Fatalf("the first run was refused: %v", err)
	}

	if _, err := strict.Ensure(2); !errors.Is(err, ErrTestLimitReached) {
		t.Fatalf("expected ErrTestLimitReached, got %v", err)
	}

	for id := range 3 {
		if _, err := relaxed.Ensure(id); err != nil {
			t.Fatalf("the relaxed registry refused run %d: %v", id, err)
		}
	}
}

func TestRegistryRangeSeesEveryRun(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(DefaultLimits())

	for id := range 3 {
		newRun(t, registry, id, time.Now())
	}

	seen := make(map[int]struct{})
	registry.Range(func(id int, _ *Test) { seen[id] = struct{}{} })

	if len(seen) != 3 {
		t.Fatalf("expected Range to see 3 runs, saw %d", len(seen))
	}
}
