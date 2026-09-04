package runs

import (
	"fmt"
	"time"
)

// GetData returns test data safely.
func (t *Test) GetData() []byte {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.Data
}

// SetData sets test data safely.
func (t *Test) SetData(data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.Data = data
}

// JoinCheckpoint registers a connection as a member of the named checkpoint's
// current round, creating the checkpoint the first time an identifier is seen.
// Joining twice from the same connection is a no-op: a barrier releases on
// distinct agents.
//
// The call never blocks. When this join is the one that reaches the target
// count, every member is notified before the call returns.
//
// The round's size and deadline are taken from the first agent to arrive in
// it, and a finished round is immediately replaced by an empty one, so the
// same identifier can be reused for every iteration of a looping suite.
//
// It returns [ErrCheckpointLimitReached] when a new identifier would take the
// run past limits.max_checkpoints_per_test. A timeout that is not positive is
// treated as [DefaultCheckpointTimeout]: an unbounded round is not on offer,
// and a zero deadline would otherwise fire at once and time the round out
// before the second agent could arrive.
func (t *Test) JoinCheckpoint(
	identifier string, target int, timeout time.Duration, connID ConnID,
) error {
	if timeout <= 0 {
		timeout = DefaultCheckpointTimeout
	}

	cp, err := t.ensureCheckpoint(identifier)
	if err != nil {
		return err
	}

	// t.mu is released before cp.mu is taken, and cp.mu before the broadcast
	// takes t.mu again to resolve the members: the two locks are never held at
	// the same time.
	if released := cp.join(connID, target, timeout); released != nil {
		cp.broadcastStatus(released)
	}

	return nil
}

// ensureCheckpoint gets or creates a checkpoint, refusing to create one past
// the run's checkpoint limit.
func (t *Test) ensureCheckpoint(identifier string) (*checkpoint, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if cp, ok := t.checkPoints[identifier]; ok {
		return cp, nil
	}

	if limit := t.limits.MaxCheckpointsPerTest; limit > 0 && len(t.checkPoints) >= limit {
		return nil, fmt.Errorf(
			"%w: %d checkpoints exist on this run, which is the configured maximum",
			ErrCheckpointLimitReached, len(t.checkPoints),
		)
	}

	if t.checkPoints == nil {
		t.checkPoints = make(map[string]*checkpoint)
	}

	cp := newCheckpoint(t, identifier)
	t.checkPoints[identifier] = cp

	return cp, nil
}
