package runs

import "time"

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
func (t *Test) JoinCheckpoint(
	identifier string, target int, timeout time.Duration, connID ConnID,
) {
	cp := t.ensureCheckpoint(identifier)

	// t.mu is released before cp.mu is taken, and cp.mu before the broadcast
	// takes t.mu again to resolve the members: the two locks are never held at
	// the same time.
	if released := cp.join(connID, target, timeout); released != nil {
		cp.broadcastStatus(released)
	}
}

// ensureCheckpoint gets or creates a checkpoint.
func (t *Test) ensureCheckpoint(identifier string) *checkpoint {
	t.mu.Lock()
	defer t.mu.Unlock()

	if cp, ok := t.checkPoints[identifier]; ok {
		return cp
	}

	if t.checkPoints == nil {
		t.checkPoints = make(map[string]*checkpoint)
	}

	cp := newCheckpoint(t, identifier)
	t.checkPoints[identifier] = cp

	return cp
}
