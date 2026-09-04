package runs

import (
	"testing"
	"time"

	"github.com/paulsgrudups/testsync/internal/storagetest"
)

// newTestSetup builds a registry and a service that belong to this test alone.
//
// Tests used to reset four package globals between them, which made every one
// of them order-dependent and none of them parallel-safe (TEST-2, CONC-12).
// Nothing here is shared, so a test can no longer be affected by the one that
// ran before it.
func newTestSetup(t *testing.T, limits Limits) (*Registry, *Service) {
	t.Helper()

	registry := NewRegistry(limits)

	return registry, NewService(storagetest.NewStore(t), registry)
}

// newRun registers a run with a chosen creation time. Registry.Ensure stamps
// the current time, which is what production wants and what a retention test
// has to override.
func newRun(t *testing.T, registry *Registry, id int, created time.Time) *Test {
	t.Helper()

	run, err := registry.Ensure(id)
	if err != nil {
		t.Fatalf("failed to register run %d: %v", id, err)
	}

	run.Created = created

	return run
}
