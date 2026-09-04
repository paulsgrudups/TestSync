package runs

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/paulsgrudups/testsync/wsutil"
)

// newJanitorTest builds a registry, a service and a janitor for one sweep
// test. Nothing is shared between tests, so a sweep can no longer reclaim
// another test's runs.
func newJanitorTest(
	t *testing.T, interval, retention time.Duration,
) (*Registry, *Service, *Janitor) {
	t.Helper()

	registry, service := newTestSetup(t, DefaultLimits())

	return registry, service, NewJanitor(interval, retention, registry, service)
}

// TestJanitorKeepsRunsWithConnectedAgents is the STAB-4 regression test.
//
// The sweep deleted a run purely by age. A suite still running after the
// retention window had its aggregate deleted while its reader goroutines held
// the old pointer, so the agents that arrived afterwards ended up on a second,
// empty run: different connection counts, and barriers the two halves could
// not join.
func TestJanitorKeepsRunsWithConnectedAgents(t *testing.T) {
	t.Parallel()

	const (
		busy = 1
		idle = 2
	)

	registry, service, janitor := newJanitorTest(t, time.Hour, 12*time.Hour)

	expired := time.Now().Add(-24 * time.Hour)

	busyRun := newRun(t, registry, busy, expired)

	if _, err := busyRun.AddConnection(wsutil.NewClient(nil)); err != nil {
		t.Fatalf("failed to attach connection: %v", err)
	}

	newRun(t, registry, idle, expired)

	if err := service.store.SaveData(busy, []byte("in use")); err != nil {
		t.Fatalf("failed to save data: %v", err)
	}

	janitor.Sweep(time.Now())

	if _, ok := registry.Get(busy); !ok {
		t.Fatal("a run with a connected agent was deleted by the sweep")
	}

	if _, ok, err := service.store.LoadData(busy); err != nil || !ok {
		t.Fatalf("the data of a run with a connected agent was deleted: ok=%v err=%v", ok, err)
	}

	if _, ok := registry.Get(idle); ok {
		t.Fatal("an expired run with no agents survived the sweep")
	}
}

// TestJanitorKeepsRunsInsideRetention covers the other half: a recent run is
// not swept, however few agents it has.
func TestJanitorKeepsRunsInsideRetention(t *testing.T) {
	t.Parallel()

	registry, _, janitor := newJanitorTest(t, time.Hour, time.Hour)

	newRun(t, registry, 3, time.Now())

	janitor.Sweep(time.Now())

	if _, ok := registry.Get(3); !ok {
		t.Fatal("a run inside its retention window was deleted")
	}
}

// TestJanitorSweepsOnceAtStartup covers the reclamation of state left behind
// by a previous process: the sweep used to run for the first time one full
// interval (12 hours) after startup.
func TestJanitorSweepsOnceAtStartup(t *testing.T) {
	t.Parallel()

	registry, _, janitor := newJanitorTest(t, time.Hour, time.Hour)

	newRun(t, registry, 4, time.Now().Add(-24*time.Hour))

	janitor.Start(t.Context())

	defer janitor.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := registry.Get(4); !ok {
			return
		}

		if time.Now().After(deadline) {
			t.Fatal("the janitor did not sweep at startup")
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// TestJanitorStopEndsItsGoroutine covers STAB-5: the sweep is owned by the
// process, so it can be stopped. Route registration used to start a ticker
// that had no shutdown path at all.
func TestJanitorStopEndsItsGoroutine(t *testing.T) {
	// Not parallel: it counts goroutines, which only means anything while
	// nothing else in this package is starting any.
	_, _, janitor := newJanitorTest(t, 10*time.Millisecond, time.Hour)

	before := runtime.NumGoroutine()

	janitor.Start(context.Background())

	stopped := make(chan struct{})

	go func() {
		janitor.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("the janitor did not stop within 1s of being asked")
	}

	waitForGoroutines(t, before)
}

// TestJanitorStopIsSafeWhenNeverStarted keeps shutdown honest: it runs even
// when startup failed before the janitor was started.
func TestJanitorStopIsSafeWhenNeverStarted(t *testing.T) {
	t.Parallel()

	_, _, janitor := newJanitorTest(t, time.Hour, time.Hour)

	janitor.Stop()
	janitor.Stop()
}

// waitForGoroutines polls until the goroutine count returns to baseline.
func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)

	for {
		count := runtime.NumGoroutine()
		if count <= baseline {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("goroutine count did not return to %d, still %d", baseline, count)
		}

		time.Sleep(10 * time.Millisecond)
	}
}
