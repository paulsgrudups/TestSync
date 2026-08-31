package runs

import "github.com/paulsgrudups/testsync/wsutil"

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

// AddConnection appends a connection and returns its index.
func (t *Test) AddConnection(conn *wsutil.Client) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.Connections = append(t.Connections, conn)
	return len(t.Connections) - 1
}

// GetConnection returns a connection by index.
func (t *Test) GetConnection(idx int) *wsutil.Client {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if idx < 0 || idx >= len(t.Connections) {
		return nil
	}

	return t.Connections[idx]
}

// ConnectionCount returns the number of connections.
func (t *Test) ConnectionCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return len(t.Connections)
}

// GetConnectionsSnapshot returns a snapshot of connections.
func (t *Test) GetConnectionsSnapshot() []*wsutil.Client {
	t.mu.RLock()
	defer t.mu.RUnlock()

	conns := make([]*wsutil.Client, len(t.Connections))
	copy(conns, t.Connections)

	return conns
}

// JoinCheckpoint registers a connection as a member of the named checkpoint,
// creating the checkpoint the first time an identifier is seen. Joining twice
// from the same connection is a no-op: a barrier releases on distinct agents.
//
// The call never blocks. When this join is the one that reaches the target
// count, every member is notified before the call returns.
//
// It reports whether the checkpoint had already been released beforehand, in
// which case the caller arrived too late to take part in it.
func (t *Test) JoinCheckpoint(identifier string, target, connIdx int) bool {
	cp := t.ensureCheckpoint(identifier, target)

	// t.mu is released before cp.mu is taken, and cp.mu before the broadcast
	// takes t.mu again for a connection snapshot: the two locks are never held
	// at the same time.
	alreadyReleased, notify := cp.join(connIdx)
	if len(notify) > 0 {
		cp.broadcastStatus(t, notify)
	}

	return alreadyReleased
}

// ensureCheckpoint gets or creates a checkpoint.
func (t *Test) ensureCheckpoint(identifier string, target int) *checkpoint {
	t.mu.Lock()
	defer t.mu.Unlock()

	if cp, ok := t.checkPoints[identifier]; ok {
		return cp
	}

	if t.checkPoints == nil {
		t.checkPoints = make(map[string]*checkpoint)
	}

	cp := newCheckpoint(identifier, target)
	t.checkPoints[identifier] = cp

	return cp
}
