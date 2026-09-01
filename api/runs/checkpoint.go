// Package runs contains test run and checkpoint logic.
package runs

import (
	"maps"
	"slices"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"
)

const (
	// checkpointLeadTime is how far in the future the agents are told to
	// resume, leaving every participant time to receive the broadcast first.
	checkpointLeadTime = 500 * time.Millisecond

	// DefaultCheckpointTimeout bounds a round whose client did not ask for a
	// deadline of its own. Without one, a single agent that never arrives
	// holds every other agent on the barrier until the CI job is killed
	// (CONC-6).
	DefaultCheckpointTimeout = 60 * time.Second

	// MaxCheckpointTimeout is the longest round the server honours. A larger
	// request is clamped to it rather than rejected, so a client that asks for
	// "forever" still gets a barrier that ends.
	MaxCheckpointTimeout = 30 * time.Minute
)

// Reason... describes why a checkpoint round ended. It is reported to every
// participant so that an agent can tell a synchronized run from an abandoned
// one instead of guessing (CONC-6).
const (
	// ReasonComplete means every expected agent arrived. It is the only reason
	// that reports finished: true.
	ReasonComplete = "complete"

	// ReasonTimeout means the round ran out of time before its target was
	// reached.
	ReasonTimeout = "timeout"

	// ReasonParticipantLost means a connection went away and left the round
	// with fewer agents than it needs.
	ReasonParticipantLost = "participant_lost"
)

// checkpointStatus is the payload every participant of a round receives when
// it ends. Identifier, Finished and StartAt are the original wire fields and
// keep their meaning exactly; the remaining four were added for CONC-6 and
// CONC-8 and are safe for an older client to ignore.
type checkpointStatus struct {
	Identifier string `json:"identifier"`
	Finished   bool   `json:"finished"`
	StartAt    int64  `json:"start_at"`
	Reason     string `json:"reason"`
	Generation int    `json:"generation"`
	Joined     int    `json:"joined"`
	Target     int    `json:"target"`
}

// release describes a round that has just ended and the members to notify. It
// is produced under the checkpoint's lock and consumed outside it, so that no
// network write ever happens while the barrier is locked.
type release struct {
	reason     string
	generation int
	joined     int
	target     int
	members    []ConnID
}

// checkpoint is a reusable barrier: it releases every member of the current
// round as soon as targetCount distinct connections have joined it, and then
// starts a fresh round on the same identifier. A looping suite therefore
// reuses one identifier instead of inventing "sync-1", "sync-2", ... per
// iteration, which used to be the only way to avoid being released
// immediately from round two onwards (CONC-8).
//
// It owns no goroutine of its own: the join that reaches the target does the
// broadcast itself, and the only other way a round can end is its deadline
// firing (CONC-6) or a participant disappearing (CONC-5).
type checkpoint struct {
	identifier string

	// test is the aggregate this barrier belongs to. It is needed to resolve
	// members to connections when a round ends without a caller at hand, as
	// happens on a timeout.
	test *Test

	mu sync.Mutex
	// generation counts rounds from one. It is reported to the participants
	// so a looping client can tell one round's release from the next.
	generation int
	// targetCount is fixed by the first agent to arrive in a round.
	targetCount int
	members     map[ConnID]struct{}
	// timer ends the round when its deadline passes. It exists only while a
	// round has members.
	timer *time.Timer
}

// newCheckpoint creates a barrier on a test. Its first round is sized by the
// first agent that joins it.
func newCheckpoint(t *Test, identifier string) *checkpoint {
	log.Infof("Creating new checkpoint %q", identifier)

	return &checkpoint{
		identifier: identifier,
		test:       t,
		generation: 1,
		members:    make(map[ConnID]struct{}),
	}
}

// CheckpointTimeout turns a client-supplied round timeout in milliseconds into
// a duration the server is willing to wait. Zero, a missing field and any
// nonsense value fall back to DefaultCheckpointTimeout; anything longer than
// MaxCheckpointTimeout is clamped to it.
func CheckpointTimeout(milliseconds int64) time.Duration {
	switch {
	case milliseconds <= 0:
		return DefaultCheckpointTimeout
	case milliseconds >= MaxCheckpointTimeout.Milliseconds():
		return MaxCheckpointTimeout
	default:
		return time.Duration(milliseconds) * time.Millisecond
	}
}

// join adds a connection to the barrier's current round. It never blocks. It
// returns the round to broadcast when this join is the one that reached the
// target, and nil while the round is still waiting.
//
// The first agent of a round fixes both its size and its deadline: the agents
// of one round are expected to agree on them, and "whoever arrived first wins"
// is at least deterministic. The deadline is measured from that first arrival.
func (cp *checkpoint) join(connID ConnID, target int, timeout time.Duration) *release {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	if len(cp.members) == 0 {
		cp.targetCount = target
		cp.startTimerLocked(timeout)
	}

	log.Debugf(
		"Adding connection to checkpoint %q round %d", cp.identifier, cp.generation,
	)

	// A set, not a slice: one connection joining twice is still one agent.
	cp.members[connID] = struct{}{}

	if len(cp.members) < cp.targetCount {
		return nil
	}

	log.Debug("Connection target reached - broadcasting")

	return cp.endRoundLocked(ReasonComplete)
}

// leave drops a connection from the current round, whether or not it had
// joined, and reports a round that can no longer succeed without it. remaining
// is the number of connections still registered on the test.
//
// A round is only abandoned when the connections that are left cannot reach
// its target. Losing one member while enough other agents are still connected
// simply frees its slot for them: the barrier counts distinct agents, not
// particular ones.
func (cp *checkpoint) leave(connID ConnID, remaining int) *release {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	delete(cp.members, connID)

	// No round is in progress, so there is nobody waiting and nothing to end.
	if len(cp.members) == 0 {
		cp.stopTimerLocked()
		return nil
	}

	if remaining >= cp.targetCount {
		return nil
	}

	log.Warnf(
		"Checkpoint %q lost a participant: %d of %d agents remain connected",
		cp.identifier, remaining, cp.targetCount,
	)

	return cp.endRoundLocked(ReasonParticipantLost)
}

// expire ends a round whose deadline passed. generation identifies the round
// the timer was started for, so a timer that fires while it is being stopped
// cannot end the round that follows.
func (cp *checkpoint) expire(generation int) {
	defer utils.RecoverGoroutine("checkpoint deadline")

	cp.mu.Lock()

	if cp.generation != generation || len(cp.members) == 0 {
		cp.mu.Unlock()
		return
	}

	released := cp.endRoundLocked(ReasonTimeout)

	cp.mu.Unlock()

	log.Warnf(
		"Checkpoint %q timed out with %d of %d agents",
		cp.identifier, released.joined, released.target,
	)

	cp.broadcastStatus(released)
}

// endRoundLocked closes the current round, snapshots what the participants
// need to be told, and opens the next one. It must be called with cp.mu held.
func (cp *checkpoint) endRoundLocked(reason string) *release {
	released := &release{
		reason:     reason,
		generation: cp.generation,
		joined:     len(cp.members),
		target:     cp.targetCount,
		members:    slices.Collect(maps.Keys(cp.members)),
	}

	cp.stopTimerLocked()

	// The next round starts clean, on the same identifier: this is what makes
	// the barrier reusable (CONC-8).
	cp.members = make(map[ConnID]struct{})
	cp.generation++

	return released
}

// startTimerLocked arms the deadline of the round that is starting. It must be
// called with cp.mu held.
func (cp *checkpoint) startTimerLocked(timeout time.Duration) {
	generation := cp.generation

	cp.timer = time.AfterFunc(timeout, func() { cp.expire(generation) })
}

// stopTimerLocked disarms the current round's deadline. It must be called with
// cp.mu held.
func (cp *checkpoint) stopTimerLocked() {
	if cp.timer == nil {
		return
	}

	cp.timer.Stop()
	cp.timer = nil
}

// broadcastStatus tells every member of a finished round how it ended. It must
// be called without cp.mu held, and cannot block: each message is queued on
// the receiving connection's own writer. A member that has already gone away
// is skipped rather than written to.
func (cp *checkpoint) broadcastStatus(released *release) {
	// One deadline for the whole barrier - the point of a checkpoint is that
	// the participants resume at the same moment. It is sent for every reason:
	// an agent released by a timeout or a lost peer still needs to know when
	// the others are carrying on.
	startAt := time.Now().Add(checkpointLeadTime).UnixMilli()

	for _, connID := range released.members {
		client := cp.test.GetConnection(connID)
		if client == nil {
			continue
		}

		err := wsutil.SendMessage(
			client,
			"wait_checkpoint",
			checkpointStatus{
				Identifier: cp.identifier,
				Finished:   released.reason == ReasonComplete,
				StartAt:    startAt,
				Reason:     released.reason,
				Generation: released.generation,
				Joined:     released.joined,
				Target:     released.target,
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
