// Package runs contains test run and checkpoint logic.
package runs

import (
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"
)

const (
	// DefaultReleaseLeadTime is how far in the future the agents are told to
	// resume when the operator configured nothing else. It leaves every
	// participant time to receive the release before any of them acts.
	DefaultReleaseLeadTime = utils.DefaultReleaseLeadTime

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

	// ReasonOperatorReleased means an operator ended the round from the
	// management API while it was still short of its target. It reports
	// finished: false like every reason but [ReasonComplete]: the agents
	// resume together, but the barrier was not met.
	ReasonOperatorReleased = "operator_released"
)

// checkpointStatus is the payload every participant of a round receives when
// it ends. Every field is always present (protocol v1, PROTOCOL.md).
type checkpointStatus struct {
	Identifier string `json:"identifier"`

	// Finished is true only when Reason is [ReasonComplete]: the barrier was
	// met. Every other reason still releases the agents together.
	Finished bool `json:"finished"`

	// Reason is one of the Reason... constants and nothing else, so a client
	// can switch on it.
	Reason string `json:"reason"`

	// Note is the free text an operator attached to a forced release, and
	// empty otherwise. It is for people reading a test log, never for code.
	Note string `json:"note"`

	Generation int `json:"generation"`
	Joined     int `json:"joined"`
	Target     int `json:"target"`

	// StartInMS is how long the agent should wait, from receiving this
	// message, before resuming. It is relative so that it needs no agreement
	// between the agent's clock and the server's (API-3).
	StartInMS int64 `json:"start_in_ms"`

	// ServerTimeMS is the server's clock when the message was queued, in
	// Unix milliseconds. It is for measuring skew, not for scheduling.
	ServerTimeMS int64 `json:"server_time_ms"`

	// StartAt is the absolute resume instant on the server's clock.
	//
	// Deprecated: it only works when every agent's clock agrees with the
	// server's. Wait StartInMS from receipt instead. It will be removed in
	// protocol v2.
	StartAt int64 `json:"start_at"`
}

// release describes a round that has just ended and the members to notify. It
// is produced under the checkpoint's lock and consumed outside it, so that no
// network write ever happens while the barrier is locked.
type release struct {
	reason     string
	note       string
	generation int
	joined     int
	target     int
	members    []member
}

// member is one participant of a finished round: the connection to notify,
// and the correlation id of the join it is being answered for.
type member struct {
	conn      ConnID
	requestID json.RawMessage
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
	// members maps each participant to the correlation id of its most recent
	// join, which its release echoes.
	members map[ConnID]json.RawMessage
	// timer ends the round when its deadline passes. It exists only while a
	// round has members.
	timer *time.Timer

	// log already carries the run's test_id and this barrier's identifier.
	log *slog.Logger
}

// newCheckpoint creates a barrier on a test. Its first round is sized by the
// first agent that joins it.
func newCheckpoint(t *Test, identifier string) *checkpoint {
	logger := t.logger().With("checkpoint", identifier)

	logger.Info("created a checkpoint")

	return &checkpoint{
		identifier: identifier,
		test:       t,
		log:        logger,
		generation: 1,
		members:    make(map[ConnID]json.RawMessage),
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
//
// A connection that joins twice is still one agent. Its release answers the
// later join, since that is the one a retrying client is waiting on.
func (cp *checkpoint) join(
	connID ConnID, requestID json.RawMessage, target int, timeout time.Duration,
) *release {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	if len(cp.members) == 0 {
		cp.targetCount = target
		cp.startTimerLocked(timeout)
	}

	cp.log.Debug("agent joined a checkpoint round",
		"conn_id", connID, "generation", cp.generation,
	)

	// A set, not a slice: one connection joining twice is still one agent.
	cp.members[connID] = requestID

	if len(cp.members) < cp.targetCount {
		return nil
	}

	cp.log.Debug("checkpoint target reached, releasing",
		"generation", cp.generation, "target", cp.targetCount,
	)

	return cp.endRoundLocked(ReasonComplete, "")
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

	cp.log.Warn("checkpoint lost a participant",
		"remaining", remaining, "target", cp.targetCount,
	)

	return cp.endRoundLocked(ReasonParticipantLost, "")
}

// expire ends a round whose deadline passed. generation identifies the round
// the timer was started for, so a timer that fires while it is being stopped
// cannot end the round that follows.
func (cp *checkpoint) expire(generation int) {
	defer utils.RecoverGoroutine(cp.log, "checkpoint deadline")

	cp.mu.Lock()

	if cp.generation != generation || len(cp.members) == 0 {
		cp.mu.Unlock()
		return
	}

	released := cp.endRoundLocked(ReasonTimeout, "")

	cp.mu.Unlock()

	cp.log.Warn("checkpoint round timed out",
		"joined", released.joined, "target", released.target,
	)

	cp.broadcastStatus(released)
}

// forceRelease ends the round that is in progress with
// [ReasonOperatorReleased] and the operator's note, and returns the members to
// notify. It returns nil when no round is in progress,
// which is the caller's cue that there was nobody to release.
//
// It is the operator override behind the management API. The round is ended
// through endRoundLocked like every other ending, so a forced release
// snapshots its members, disarms the deadline and opens the next round by
// exactly the same code path as a completed or expired one.
//
// The caller broadcasts, outside the lock this takes.
func (cp *checkpoint) forceRelease(note string) *release {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	if len(cp.members) == 0 {
		return nil
	}

	cp.log.Info("checkpoint round released by an operator",
		"generation", cp.generation, "joined", len(cp.members),
		"target", cp.targetCount, "note", note,
	)

	return cp.endRoundLocked(ReasonOperatorReleased, note)
}

// endRoundLocked closes the current round, snapshots what the participants
// need to be told, and opens the next one. It must be called with cp.mu held.
func (cp *checkpoint) endRoundLocked(reason, note string) *release {
	released := &release{
		reason:     reason,
		note:       note,
		generation: cp.generation,
		joined:     len(cp.members),
		target:     cp.targetCount,
		members:    make([]member, 0, len(cp.members)),
	}

	for _, connID := range slices.Sorted(maps.Keys(cp.members)) {
		released.members = append(released.members, member{
			conn: connID, requestID: cp.members[connID],
		})
	}

	cp.stopTimerLocked()

	// The next round starts clean, on the same identifier: this is what makes
	// the barrier reusable (CONC-8).
	cp.members = make(map[ConnID]json.RawMessage)
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
	// One resume instant for the whole barrier - the point of a checkpoint is
	// that the participants resume at the same moment. It is sent for every
	// reason: an agent released by a timeout or a lost peer still needs to
	// know when the others are carrying on.
	startAt := time.Now().Add(cp.test.releaseLeadTime())

	for _, m := range released.members {
		client := cp.test.GetConnection(m.conn)
		if client == nil {
			continue
		}

		// The relative delay is taken per recipient, as late as possible, so
		// an agent further down the list is not told to wait for time that has
		// already passed while the others were being queued.
		now := time.Now()

		err := wsutil.SendReply(
			client,
			"wait_checkpoint",
			m.requestID,
			checkpointStatus{
				Identifier:   cp.identifier,
				Finished:     released.reason == ReasonComplete,
				Reason:       released.reason,
				Note:         released.note,
				Generation:   released.generation,
				Joined:       released.joined,
				Target:       released.target,
				StartInMS:    max(startAt.Sub(now).Milliseconds(), 0),
				ServerTimeMS: now.UnixMilli(),
				StartAt:      startAt.UnixMilli(),
			},
		)
		if err != nil {
			cp.log.Error("could not deliver a checkpoint release", "error", err)
		}
	}
}
