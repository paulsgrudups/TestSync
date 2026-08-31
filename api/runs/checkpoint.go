// Package runs contains test run and checkpoint logic.
package runs

import (
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/paulsgrudups/testsync/wsutil"
)

// checkpointLeadTime is how far in the future the agents are told to resume,
// leaving every participant time to receive the broadcast first.
const checkpointLeadTime = 500 * time.Millisecond

// checkpoint is a one-shot barrier: it releases every member as soon as
// targetCount distinct connections have joined. It owns no goroutine, so there
// is nothing to wake up, nothing to shut down and nowhere for a joining agent
// to block: the join that reaches the target does the broadcast itself.
type checkpoint struct {
	identifier  string
	targetCount int

	mu       sync.Mutex
	members  map[int]struct{}
	released chan struct{}
}

// newCheckpoint creates a barrier that fires once target distinct connections
// have joined it.
func newCheckpoint(identifier string, target int) *checkpoint {
	log.Infof("Creating new checkpoint %q", identifier)

	return &checkpoint{
		identifier:  identifier,
		targetCount: target,
		members:     make(map[int]struct{}),
		released:    make(chan struct{}),
	}
}

// join adds a connection to the barrier's member set. It never blocks. It
// reports whether the barrier had already been released before this call, and
// the members to notify when this join is the one that reached the target.
func (cp *checkpoint) join(connIdx int) (bool, []int) {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	select {
	case <-cp.released:
		return true, nil
	default:
	}

	log.Debugf("Adding connection to checkpoint %q", cp.identifier)

	// A set, not a slice: one connection joining twice is still one agent.
	cp.members[connIdx] = struct{}{}

	if len(cp.members) < cp.targetCount {
		return false, nil
	}

	log.Debug("Connection target reached - broadcasting")

	// Closing releases the barrier exactly once: every later join takes the
	// branch above instead.
	close(cp.released)

	notify := make([]int, 0, len(cp.members))
	for idx := range cp.members {
		notify = append(notify, idx)
	}

	return false, notify
}

// broadcastStatus tells every member that the barrier has fired. It must be
// called without cp.mu held, and cannot block: each message is queued on the
// receiving connection's own writer.
func (cp *checkpoint) broadcastStatus(t *Test, members []int) {
	connections := t.GetConnectionsSnapshot()

	// One deadline for the whole barrier - the point of a checkpoint is that
	// the participants resume at the same moment.
	startAt := time.Now().Add(checkpointLeadTime).UnixMilli()

	for _, idx := range members {
		if idx < 0 || idx >= len(connections) {
			continue
		}

		err := wsutil.SendMessage(
			connections[idx],
			"wait_checkpoint",
			struct {
				Identifier string `json:"identifier"`
				Finished   bool   `json:"finished"`
				StartAt    int64  `json:"start_at"`
			}{
				Identifier: cp.identifier,
				Finished:   true,
				StartAt:    startAt,
			},
		)
		if err != nil {
			log.Errorf(
				"Could not broadcast message to checkpoint %q: %s",
				cp.identifier, err.Error(),
			)
		}
	}
}
