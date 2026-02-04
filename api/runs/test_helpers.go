package runs

import "github.com/gorilla/websocket"

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
func (t *Test) AddConnection(conn *websocket.Conn) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.Connections = append(t.Connections, conn)
	return len(t.Connections) - 1
}

// GetConnection returns a connection by index.
func (t *Test) GetConnection(idx int) *websocket.Conn {
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
func (t *Test) GetConnectionsSnapshot() []*websocket.Conn {
	t.mu.RLock()
	defer t.mu.RUnlock()

	conns := make([]*websocket.Conn, len(t.Connections))
	copy(conns, t.Connections)

	return conns
}

// EnsureCheckpoint gets or creates a checkpoint.
func (t *Test) EnsureCheckpoint(identifier string, target int) *Checkpoint {
	t.mu.Lock()
	defer t.mu.Unlock()

	if cp, ok := t.CheckPoints[identifier]; ok {
		return cp
	}

	if t.CheckPoints == nil {
		t.CheckPoints = make(map[string]*Checkpoint)
	}

	cp := CreateCheckpoint(identifier, target, t)
	t.CheckPoints[identifier] = cp

	return cp
}
