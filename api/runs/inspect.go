package runs

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// ConnectionState is a point-in-time, read-only view of one agent connection.
// A run drops a connection once its reader exits, so a state that is reported
// here belongs to an agent that was still registered at the moment of the
// snapshot.
type ConnectionState struct {
	// Ordinal numbers the connection within its run, from zero, in arrival
	// order. It is the short handle an operator sees. Ordinals are not reused,
	// so a gap means an agent left.
	Ordinal int

	// ID is the process-wide connection identifier, which is what the server
	// logs. It is carried so a UI can be correlated with a log line.
	ID ConnID

	// Active reports whether the connection is still writable. A connection
	// whose writer has already finished is on its way out of the run.
	Active bool
}

// CheckpointState is a point-in-time, read-only view of one checkpoint
// barrier. It carries no test data, only the barrier's own bookkeeping.
//
// A checkpoint is reusable: it runs a numbered sequence of rounds on the same
// identifier, so it is never permanently "finished". At any moment it is
// either waiting for the current round to fill, or idle between rounds.
type CheckpointState struct {
	// Identifier is the name the agents agreed on. It is caller supplied and
	// may contain anything, so consumers must treat it as untrusted text.
	Identifier string

	// Generation is the round currently in progress, counted from one.
	Generation int

	// RoundsCompleted is how many rounds have already ended, by any reason.
	RoundsCompleted int

	// TargetCount is how many distinct agents the current round waits for. It
	// is fixed by the round's first arrival, so it is zero while idle.
	TargetCount int

	// Members holds the ordinals of the agents that have joined the current
	// round, ascending. It is empty while idle.
	Members []int

	// Waiting reports whether a round is in progress and short of its target.
	Waiting bool
}

// TestState is a point-in-time, read-only view of one test run. It reports
// sizes and counts only: stored test data can hold anything a caller put in
// it and is deliberately never copied out of the run.
type TestState struct {
	// TestID is the run's identifier.
	TestID int

	// Created is when the run was first seen.
	Created time.Time

	// DataSize is the length in bytes of the data cached with the run.
	DataSize int

	// ForceEnd mirrors the run's force-end flag.
	ForceEnd bool

	// Connections holds the agents currently attached to the run.
	Connections []ConnectionState

	// Checkpoints holds every checkpoint of the run, ordered by identifier.
	Checkpoints []CheckpointState
}

// AllTestStates returns a snapshot of every known test run, ordered by test
// ID. It is read-only: nothing it returns aliases live run state, and taking
// it never releases a barrier nor mutates a run.
func AllTestStates() []TestState {
	states := make([]TestState, 0)

	RangeTests(func(id int, t *Test) {
		states = append(states, t.State(id))
	})

	slices.SortFunc(states, func(a, b TestState) int {
		return a.TestID - b.TestID
	})

	return states
}

// State returns a read-only snapshot of the run. The caller supplies the test
// ID because a run does not carry its own key.
//
// The run's lock and the checkpoint locks are never held at the same time,
// matching the ordering the barrier code relies on.
func (t *Test) State(testID int) TestState {
	t.mu.RLock()

	state := TestState{
		TestID:      testID,
		Created:     t.Created,
		DataSize:    len(t.Data),
		ForceEnd:    t.ForceEnd,
		Connections: make([]ConnectionState, 0, len(t.connections)),
		Checkpoints: make([]CheckpointState, 0, len(t.checkPoints)),
	}

	for id, conn := range t.connections {
		state.Connections = append(state.Connections, ConnectionState{
			Ordinal: t.ordinals[id],
			ID:      id,
			Active:  !conn.Closed(),
		})
	}

	// Resolving a member ordinal needs the run's map, so it is copied out here
	// rather than reaching back into the run while a checkpoint lock is held.
	ordinals := make(map[ConnID]int, len(t.ordinals))
	maps.Copy(ordinals, t.ordinals)

	points := make([]*checkpoint, 0, len(t.checkPoints))
	for _, cp := range t.checkPoints {
		points = append(points, cp)
	}

	t.mu.RUnlock()

	slices.SortFunc(state.Connections, func(a, b ConnectionState) int {
		return a.Ordinal - b.Ordinal
	})

	for _, cp := range points {
		state.Checkpoints = append(state.Checkpoints, cp.state(ordinals))
	}

	slices.SortFunc(state.Checkpoints, func(a, b CheckpointState) int {
		return strings.Compare(a.Identifier, b.Identifier)
	})

	return state
}

// state returns a read-only snapshot of the barrier. Member ordinals are
// resolved through the caller-supplied map so that the run's lock is not
// needed while cp.mu is held.
func (cp *checkpoint) state(ordinals map[ConnID]int) CheckpointState {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	members := make([]int, 0, len(cp.members))

	for id := range cp.members {
		// A member whose connection has just been removed no longer has an
		// ordinal; it is on its way out of the round too.
		if ordinal, ok := ordinals[id]; ok {
			members = append(members, ordinal)
		}
	}

	slices.Sort(members)

	return CheckpointState{
		Identifier:      cp.identifier,
		Generation:      cp.generation,
		RoundsCompleted: cp.generation - 1,
		TargetCount:     cp.targetCount,
		Members:         members,
		Waiting:         len(cp.members) > 0,
	}
}
