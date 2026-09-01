package runs

import (
	"errors"
	"testing"
	"time"

	"github.com/paulsgrudups/testsync/internal/storagetest"
	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"
)

// withLimits installs limits for one test and restores them afterwards.
func withLimits(t *testing.T, limits Limits) {
	t.Helper()

	previous := CurrentLimits()
	t.Cleanup(func() { SetLimits(previous) })

	SetLimits(limits)
}

// TestMaxTestsRejectsNewRuns covers STAB-3: the registry grew by one entry per
// test ID ever seen, so any client could exhaust the server's memory just by
// counting upwards.
func TestMaxTestsRejectsNewRuns(t *testing.T) {
	AllTests = make(map[int]*Test)

	limits := DefaultLimits()
	limits.MaxTests = 2
	withLimits(t, limits)

	for id := range 2 {
		if _, err := EnsureTest(id, func() *Test { return &Test{Created: time.Now()} }); err != nil {
			t.Fatalf("run %d was refused below the limit: %v", id, err)
		}
	}

	_, err := EnsureTest(99, func() *Test { return &Test{Created: time.Now()} })
	if !errors.Is(err, ErrTestLimitReached) {
		t.Fatalf("expected ErrTestLimitReached, got %v", err)
	}

	if err := CanAdmitTest(99); !errors.Is(err, ErrTestLimitReached) {
		t.Fatalf("expected CanAdmitTest to refuse a new run, got %v", err)
	}

	// A run that already exists is always admitted: the limit bounds how many
	// runs exist, not how many agents may reach one.
	if err := CanAdmitTest(0); err != nil {
		t.Fatalf("an existing run was refused: %v", err)
	}

	if _, err := EnsureTest(0, func() *Test { return &Test{} }); err != nil {
		t.Fatalf("an existing run was refused: %v", err)
	}

	if count := TestCount(); count != 2 {
		t.Fatalf("expected 2 runs, got %d", count)
	}
}

// TestMaxConnectionsPerTestRejectsAgents covers the per-run connection cap.
func TestMaxConnectionsPerTestRejectsAgents(t *testing.T) {
	AllTests = make(map[int]*Test)

	limits := DefaultLimits()
	limits.MaxConnectionsPerTest = 2
	withLimits(t, limits)

	run := &Test{Created: time.Now()}
	SetTest(1, run)

	for i := range 2 {
		if _, err := run.AddConnection(wsutil.NewClient(nil)); err != nil {
			t.Fatalf("agent %d was refused below the limit: %v", i, err)
		}
	}

	id, err := run.AddConnection(wsutil.NewClient(nil))
	if !errors.Is(err, ErrConnectionLimitReached) {
		t.Fatalf("expected ErrConnectionLimitReached, got %v", err)
	}

	if id != 0 {
		t.Fatalf("a refused connection was given the identity %d", id)
	}

	if count := run.ConnectionCount(); count != 2 {
		t.Fatalf("expected 2 connections, got %d", count)
	}

	// A slot freed by a departing agent is usable again.
	for connID := range run.connections {
		run.RemoveConnection(connID)
		break
	}

	if _, err := run.AddConnection(wsutil.NewClient(nil)); err != nil {
		t.Fatalf("a freed slot was not reusable: %v", err)
	}
}

// TestMaxCheckpointsPerTestRejectsNewIdentifiers covers the per-run barrier
// cap. Checkpoints were created on demand and never pruned, so a suite that
// invented an identifier per iteration grew the run without bound.
func TestMaxCheckpointsPerTestRejectsNewIdentifiers(t *testing.T) {
	AllTests = make(map[int]*Test)

	limits := DefaultLimits()
	limits.MaxCheckpointsPerTest = 2
	withLimits(t, limits)

	run := &Test{Created: time.Now()}
	SetTest(1, run)

	connID, err := run.AddConnection(wsutil.NewClient(nil))
	if err != nil {
		t.Fatalf("failed to attach connection: %v", err)
	}

	for _, identifier := range []string{"round-1", "round-2"} {
		if err := run.JoinCheckpoint(identifier, 5, time.Minute, connID); err != nil {
			t.Fatalf("checkpoint %q was refused below the limit: %v", identifier, err)
		}
	}

	err = run.JoinCheckpoint("round-3", 5, time.Minute, connID)
	if !errors.Is(err, ErrCheckpointLimitReached) {
		t.Fatalf("expected ErrCheckpointLimitReached, got %v", err)
	}

	// An identifier that already exists still works: the limit bounds how many
	// barriers a run may have, not how often they may be used.
	if err := run.JoinCheckpoint("round-1", 5, time.Minute, connID); err != nil {
		t.Fatalf("an existing checkpoint was refused: %v", err)
	}

	run.mu.RLock()
	count := len(run.checkPoints)
	run.mu.RUnlock()

	if count != 2 {
		t.Fatalf("expected 2 checkpoints, got %d", count)
	}
}

// TestMaxDataBytesRejectsOversizedPayloads covers the stored-payload cap. It
// is the same number as the HTTP body cap and the WebSocket frame cap, so a
// payload cannot be accepted on one path and refused on another.
func TestMaxDataBytesRejectsOversizedPayloads(t *testing.T) {
	AllTests = make(map[int]*Test)

	limits := DefaultLimits()
	limits.MaxDataBytes = 16
	withLimits(t, limits)

	service := NewService(storagetest.NewStore(t))

	oversized := make([]byte, 17)

	if err := service.CreateTestData(1, oversized); !errors.Is(err, ErrDataTooLarge) {
		t.Fatalf("expected ErrDataTooLarge from create, got %v", err)
	}

	if err := service.UpdateTestData(1, oversized); !errors.Is(err, ErrDataTooLarge) {
		t.Fatalf("expected ErrDataTooLarge from update, got %v", err)
	}

	// Nothing was stored and no run was registered for the refused payload.
	if _, err := service.ReadTestData(1); !errors.Is(err, ErrTestNotFound) {
		t.Fatalf("a refused payload was stored: %v", err)
	}

	if _, ok := GetTest(1); ok {
		t.Fatal("a refused payload registered a run")
	}

	if err := service.CreateTestData(1, make([]byte, 16)); err != nil {
		t.Fatalf("a payload at the limit was refused: %v", err)
	}
}

// TestLimitsFromConfig covers the configuration mapping: an omitted field
// keeps its default, a configured one is honoured.
func TestLimitsFromConfig(t *testing.T) {
	limits := LimitsFromConfig(utils.LimitsConfig{MaxTests: 7})

	if limits.MaxTests != 7 {
		t.Fatalf("expected max_tests 7, got %d", limits.MaxTests)
	}

	if limits.MaxDataBytes != utils.DefaultMaxDataBytes {
		t.Fatalf(
			"expected the default max_data_bytes, got %d", limits.MaxDataBytes,
		)
	}
}

// TestCurrentLimitsDefaultsWithoutSetup covers the fail-safe: a process that
// never installed limits is bounded by the defaults rather than unbounded.
func TestCurrentLimitsDefaultsWithoutSetup(t *testing.T) {
	previous := current.Swap(nil)
	t.Cleanup(func() { current.Store(previous) })

	if got := CurrentLimits(); got != DefaultLimits() {
		t.Fatalf("expected the default limits, got %+v", got)
	}
}
