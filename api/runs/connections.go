package runs

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync/atomic"

	log "github.com/sirupsen/logrus"

	"github.com/paulsgrudups/testsync/wsutil"
)

// ConnID identifies one WebSocket connection. It is minted at registration and
// never reused, which is what lets a connection be removed at all: the old
// identity was a position in an append-only slice, so removing anything would
// have silently renamed every connection after it (CONC-5).
type ConnID uint64

// nextConnID mints connection identifiers. It is process-wide rather than
// per-test so that an ID identifies a connection on its own in a log line.
var nextConnID atomic.Uint64

// AddConnection registers a connection and returns the ID that identifies it
// for the rest of its life. The caller owns that ID and must hand it to
// RemoveConnection when the connection's reader exits.
//
// A run that already holds limits.max_connections_per_test agents refuses the
// connection with [ErrConnectionLimitReached] rather than growing without
// bound (STAB-3). The check and the insert happen under the same lock, so
// agents arriving together cannot race past the limit.
func (t *Test) AddConnection(conn *wsutil.Client) (ConnID, error) {
	id := ConnID(nextConnID.Add(1))

	t.mu.Lock()
	defer t.mu.Unlock()

	if limit := CurrentLimits().MaxConnectionsPerTest; limit > 0 && len(t.connections) >= limit {
		return 0, fmt.Errorf(
			"%w: %d agents are attached, which is the configured maximum",
			ErrConnectionLimitReached, len(t.connections),
		)
	}

	if t.connections == nil {
		t.connections = make(map[ConnID]*wsutil.Client)
	}

	if t.ordinals == nil {
		t.ordinals = make(map[ConnID]int)
	}

	t.connections[id] = conn
	t.ordinals[id] = t.nextOrdinal
	t.nextOrdinal++

	return id, nil
}

// RemoveConnection deregisters a connection and drops it from every checkpoint
// it had joined. A round that the surviving connections can no longer complete
// is ended immediately with ReasonParticipantLost, rather than left waiting for
// an agent that is never coming back (CONC-6).
func (t *Test) RemoveConnection(id ConnID) {
	t.mu.Lock()

	if _, ok := t.connections[id]; !ok {
		t.mu.Unlock()
		return
	}

	delete(t.connections, id)
	// The ordinal is not reused: a gap is the honest record that an agent was
	// here and left.
	delete(t.ordinals, id)

	remaining := len(t.connections)
	points := slices.Collect(maps.Values(t.checkPoints))

	t.mu.Unlock()

	log.Debugf("Removed connection %d, %d remaining", id, remaining)

	// Neither t.mu nor cp.mu is held here: ending a round writes to the
	// surviving connections, and no lock may be held across a write.
	for _, cp := range points {
		if released := cp.leave(id, remaining); released != nil {
			cp.broadcastStatus(released)
		}
	}
}

// GetConnection returns a connection by ID, or nil once it has gone away.
func (t *Test) GetConnection(id ConnID) *wsutil.Client {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.connections[id]
}

// ConnectionCount returns the number of connections currently registered. It
// counts live connections only: a disconnected agent stops being counted
// within one command round-trip of its reader exiting.
func (t *Test) ConnectionCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return len(t.connections)
}

// GetConnectionsSnapshot returns the connections currently registered, in no
// particular order.
func (t *Test) GetConnectionsSnapshot() []*wsutil.Client {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return slices.Collect(maps.Values(t.connections))
}

// CloseAllConnections closes every registered agent connection with the given
// WebSocket close code and waits, until ctx expires, for the close frames to
// reach the wire.
//
// Shutdown uses it with [websocket.CloseServiceRestart] so that an agent can
// tell a deploy from a crash: http.Server.Shutdown does not track hijacked
// connections, so without this the sockets are simply dropped and every agent
// sees an abnormal closure (STAB-6).
//
// It returns how many connections were closed.
func CloseAllConnections(ctx context.Context, code int, reason string) int {
	clients := make([]*wsutil.Client, 0)

	RangeTests(func(_ int, t *Test) {
		clients = append(clients, t.GetConnectionsSnapshot()...)
	})

	for _, client := range clients {
		client.CloseWithReason(code, reason)
	}

	for _, client := range clients {
		select {
		case <-client.Finished():
		case <-ctx.Done():
			return len(clients)
		}
	}

	return len(clients)
}
