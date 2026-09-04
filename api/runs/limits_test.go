package runs

import (
	"errors"
	"testing"
	"time"

	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"
)

// limitsWith returns the default limits with one field overridden.
func limitsWith(apply func(*Limits)) Limits {
	limits := DefaultLimits()
	apply(&limits)

	return limits
}

// TestMaxTestsRejectsNewRuns covers STAB-3: the registry grew by one entry per
// test ID ever seen, so any client could exhaust the server's memory just by
// counting upwards.
func TestMaxTestsRejectsNewRuns(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(limitsWith(func(l *Limits) { l.MaxTests = 2 }))

	for id := range 2 {
		if _, err := registry.Ensure(id); err != nil {
			t.Fatalf("run %d was refused below the limit: %v", id, err)
		}
	}

	if _, err := registry.Ensure(99); !errors.Is(err, ErrTestLimitReached) {
		t.Fatalf("expected ErrTestLimitReached, got %v", err)
	}

	if err := registry.CanAdmit(99); !errors.Is(err, ErrTestLimitReached) {
		t.Fatalf("expected CanAdmit to refuse a new run, got %v", err)
	}

	// A run that already exists is always admitted: the limit bounds how many
	// runs exist, not how many agents may reach one.
	if err := registry.CanAdmit(0); err != nil {
		t.Fatalf("an existing run was refused: %v", err)
	}

	if _, err := registry.Ensure(0); err != nil {
		t.Fatalf("an existing run was refused: %v", err)
	}

	if count := registry.Count(); count != 2 {
		t.Fatalf("expected 2 runs, got %d", count)
	}
}

// TestMaxConnectionsPerTestRejectsAgents covers the per-run connection cap.
func TestMaxConnectionsPerTestRejectsAgents(t *testing.T) {
	t.Parallel()

	registry := NewRegistry(
		limitsWith(func(l *Limits) { l.MaxConnectionsPerTest = 2 }),
	)
	run := newRun(t, registry, 1, time.Now())

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
	t.Parallel()

	registry := NewRegistry(
		limitsWith(func(l *Limits) { l.MaxCheckpointsPerTest = 2 }),
	)
	run := newRun(t, registry, 1, time.Now())

	connID, err := run.AddConnection(wsutil.NewClient(nil))
	if err != nil {
		t.Fatalf("failed to attach connection: %v", err)
	}

	for _, identifier := range []string{"round-1", "round-2"} {
		if err = run.JoinCheckpoint(identifier, 5, time.Minute, connID); err != nil {
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
	t.Parallel()

	registry, service := newTestSetup(
		t, limitsWith(func(l *Limits) { l.MaxDataBytes = 16 }),
	)

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

	if _, ok := registry.Get(1); ok {
		t.Fatal("a refused payload registered a run")
	}

	if err := service.CreateTestData(1, make([]byte, 16)); err != nil {
		t.Fatalf("a payload at the limit was refused: %v", err)
	}
}

// TestLimitsFromConfig covers the configuration mapping: an omitted field
// keeps its default, a configured one is honoured.
func TestLimitsFromConfig(t *testing.T) {
	t.Parallel()

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

// TestLimitsFromEmptyConfigAreDefaults covers the fail-safe: a server whose
// operator configured no limits at all is bounded by the defaults rather than
// unbounded.
func TestLimitsFromEmptyConfigAreDefaults(t *testing.T) {
	t.Parallel()

	if got := LimitsFromConfig(utils.LimitsConfig{}); got != DefaultLimits() {
		t.Fatalf("expected the default limits, got %+v", got)
	}
}
