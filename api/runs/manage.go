package runs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"

	"github.com/paulsgrudups/testsync/utils"
)

// Failures of the run management operations. Each one names a condition an
// operator can act on, so it is reported as itself rather than folded into a
// generic 500.
var (
	// ErrRunNotFound means the server knows nothing about that test ID: it is
	// neither registered as a run nor holding a stored payload. It is not
	// [ErrTestNotFound], which is about the payload alone — a run that has
	// only ever coordinated agents exists while storing nothing.
	ErrRunNotFound = errors.New("test run not found")

	// ErrCheckpointNotFound means the run exists but has never seen that
	// checkpoint identifier. Identifiers are caller supplied, so this is
	// usually a typo rather than a server problem.
	ErrCheckpointNotFound = errors.New("checkpoint not found")

	// ErrNoRoundInProgress means the barrier exists but no agent is waiting on
	// it, so there is no round to release. A checkpoint is reusable and spends
	// its life between rounds, so this is the ordinary state, not a fault.
	ErrNoRoundInProgress = errors.New("no checkpoint round in progress")

	// ErrConnectionNotFound means the run has no connection with that ID. An
	// ID is never reused, so this means the agent has already gone away.
	ErrConnectionNotFound = errors.New("connection not found")
)

// The stable machine-readable reasons carried in an error body, so a client
// can branch on the cause instead of matching the prose that accompanies it.
//
// These name a failed request. They are not the [ReasonComplete] family, which
// names why a checkpoint round ended and travels to the agents over the
// WebSocket protocol.
const (
	// ErrorReasonRunNotFound reports [ErrRunNotFound].
	ErrorReasonRunNotFound = "run_not_found"

	// ErrorReasonCheckpointNotFound reports [ErrCheckpointNotFound].
	ErrorReasonCheckpointNotFound = "checkpoint_not_found"

	// ErrorReasonNoRoundInProgress reports [ErrNoRoundInProgress].
	ErrorReasonNoRoundInProgress = "no_round_in_progress"

	// ErrorReasonConnectionNotFound reports [ErrConnectionNotFound].
	ErrorReasonConnectionNotFound = "connection_not_found"

	// ErrorReasonDataNotFound reports [ErrTestNotFound]: the run has no stored
	// payload.
	ErrorReasonDataNotFound = "data_not_found"

	// ErrorReasonInvalidRequest reports a malformed request body.
	ErrorReasonInvalidRequest = "invalid_request"
)

const (
	// OperatorDisconnectReason is the WebSocket close reason an agent is given
	// when an operator disconnects it. The close code is a normal closure: the
	// server is working correctly and the agent is being told so, which is not
	// the same thing as a crash.
	OperatorDisconnectReason = "disconnected by operator"

	// operatorDeleteReason is the close reason the agents of a deleted run are
	// given.
	operatorDeleteReason = "run deleted by operator"

	// MaxReleaseNoteBytes bounds the note an operator attaches to a forced
	// release. The note is echoed to every agent of the round, so it is kept
	// to something a log line can hold.
	MaxReleaseNoteBytes = 128

	// ConnectionsDroppedHeader reports how many agents were attached to a run
	// that has just been deleted, so an operator page can warn that a live
	// suite was interrupted.
	//
	// It is spelled in the canonical header form the net/http header map
	// stores it under. Header names are case-insensitive, so a client that
	// asks for "X-TestSync-Connections-Dropped" still reads this one.
	ConnectionsDroppedHeader = "X-Testsync-Connections-Dropped"
)

// ReleaseResult reports what a forced release did: which round it ended, how
// many agents were waiting on it and how many it had been waiting for.
type ReleaseResult struct {
	// Identifier is the barrier that was released.
	Identifier string

	// Reason is what the agents were told: always [ReasonOperatorReleased].
	Reason string

	// Note is the operator's free text, trimmed, as the agents received it.
	Note string

	// Generation is the round that was ended, counted from one.
	Generation int

	// Released is how many agents were waiting and have now been released.
	Released int

	// Target is how many the round had been waiting for. Released is short of
	// it, or the round would have completed by itself.
	Target int
}

// ReleaseCheckpoint force-releases the round in progress on one run's
// checkpoint, reporting [ErrRunNotFound] when the run is not registered.
//
// The note reaches the agents trimmed but otherwise unchanged, so the caller is
// expected to have bounded its length already; see [MaxReleaseNoteBytes].
func (s *Service) ReleaseCheckpoint(
	testID int, identifier, note string,
) (ReleaseResult, error) {
	run, ok := s.registry.Get(testID)
	if !ok {
		return ReleaseResult{}, fmt.Errorf("%w: %d", ErrRunNotFound, testID)
	}

	return run.ReleaseCheckpoint(identifier, strings.TrimSpace(note))
}

// DisconnectAgent closes one agent's connection to a run and removes it,
// reporting [ErrRunNotFound] or [ErrConnectionNotFound].
func (s *Service) DisconnectAgent(testID int, connID ConnID) error {
	run, ok := s.registry.Get(testID)
	if !ok {
		return fmt.Errorf("%w: %d", ErrRunNotFound, testID)
	}

	return run.DisconnectConnection(connID)
}

// DeleteRun removes a run and its stored payload together, and reports how
// many agents were attached to it.
//
// Deleting a run that still has agents is deliberately allowed: it is an
// operator override, and refusing would leave no way to clear a run whose
// agents are wedged. Those agents are disconnected rather than left attached
// to an aggregate that is no longer registered — a reconnecting agent would
// otherwise create a second run for the same ID and the two halves of a suite
// would disagree about who is on which barrier (STAB-4).
//
// A test ID that is neither registered nor stored reports [ErrRunNotFound]. A
// stored payload with no run is still deletable: the runs are in memory and
// the payloads outlive a restart, so the two are not always both present.
func (s *Service) DeleteRun(ctx context.Context, testID int) (int, error) {
	run, registered := s.registry.Get(testID)

	_, stored, err := s.store.DataSize(ctx, testID)
	if err != nil {
		return 0, fmt.Errorf("could not check for stored data: %w", err)
	}

	if !registered && !stored {
		return 0, fmt.Errorf("%w: %d", ErrRunNotFound, testID)
	}

	dropped := 0

	if registered {
		// Unregistered first: an agent arriving during the delete then gets a
		// fresh run rather than joining the one being torn down.
		s.registry.Delete(testID)
		dropped = run.DisconnectAll(operatorDeleteReason)
	}

	if err := s.store.DeleteData(ctx, testID); err != nil {
		return dropped, fmt.Errorf("could not delete stored data: %w", err)
	}

	return dropped, nil
}

// DeleteRunHandler answers a request to delete one run with 204 and no body,
// carrying [ConnectionsDroppedHeader] so the caller learns whether it
// interrupted anything.
//
// It is exported because two routes share it: the operator-facing
// DELETE /api/v1/runs/{testID} and the agent-facing DELETE /tests/{testID} are
// the same operation under two spellings, and must not become two
// implementations that drift apart.
func (s *Service) DeleteRunHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	testID, err := GetPathID(w, r, "testID")
	if err != nil {
		s.log.DebugContext(ctx, "could not parse the test ID", "error", err)
		return
	}

	logger := s.log.With("test_id", testID)

	dropped, err := s.DeleteRun(ctx, testID)
	if err != nil {
		if errors.Is(err, ErrRunNotFound) {
			logger.DebugContext(ctx, "test run not found")
			utils.HTTPErrorReason(
				w, "Could not find test run", http.StatusNotFound,
				ErrorReasonRunNotFound,
			)

			return
		}

		logger.ErrorContext(ctx, "could not delete the test run", "error", err)
		utils.HTTPError(
			w, "Could not delete test run", http.StatusInternalServerError,
		)

		return
	}

	logger.InfoContext(ctx, "deleted a test run", "connections_dropped", dropped)

	w.Header().Set(ConnectionsDroppedHeader, strconv.Itoa(dropped))
	w.WriteHeader(http.StatusNoContent)
}

// ReleaseCheckpoint ends the round in progress on the named checkpoint and
// tells its members why, as if the round had ended on its own.
//
// It reports [ErrCheckpointNotFound] for an identifier the run has never seen,
// and [ErrNoRoundInProgress] when the barrier is idle between rounds, which is
// where a reusable checkpoint spends most of its time.
//
// The released agents are told reason [ReasonOperatorReleased] and finished:
// false: they resume together, but the barrier they were waiting for was not
// met. The note travels beside the reason, never in place of it.
func (t *Test) ReleaseCheckpoint(identifier, note string) (ReleaseResult, error) {
	t.mu.RLock()
	cp, ok := t.checkPoints[identifier]
	t.mu.RUnlock()

	if !ok {
		return ReleaseResult{}, fmt.Errorf("%w: %q", ErrCheckpointNotFound, identifier)
	}

	released := cp.forceRelease(note)
	if released == nil {
		return ReleaseResult{}, fmt.Errorf("%w: %q", ErrNoRoundInProgress, identifier)
	}

	// t.mu was dropped before cp.mu was taken inside forceRelease, and cp.mu
	// before this line: the broadcast writes to the members, and no lock may
	// be held across a write.
	cp.broadcastStatus(released)

	return ReleaseResult{
		Identifier: identifier,
		Reason:     released.reason,
		Note:       released.note,
		Generation: released.generation,
		Released:   released.joined,
		Target:     released.target,
	}, nil
}

// DisconnectConnection closes one agent's connection and removes it from the
// run, which frees its slot in every barrier it had joined, exactly as if the
// agent had gone away by itself.
//
// It reports [ErrConnectionNotFound] when the ID names no connection of this
// run, which includes an agent that has just left of its own accord.
func (t *Test) DisconnectConnection(id ConnID) error {
	client := t.GetConnection(id)
	if client == nil {
		return fmt.Errorf("%w: %d", ErrConnectionNotFound, id)
	}

	// No lock is held here, and the close does not touch the socket itself:
	// it queues the close frame for the connection's own writer, which is the
	// only goroutine allowed to write to it.
	client.CloseWithReason(websocket.CloseNormalClosure, OperatorDisconnectReason)

	// Removing is what frees the barrier slots. Waiting for the agent's own
	// reader to notice the closure would leave a round short of an agent that
	// is already gone for as long as the socket takes to unwind.
	t.RemoveConnection(id)

	t.logger().Info("disconnected an agent on request", "conn_id", id)

	return nil
}

// DisconnectAll closes every agent attached to the run, with the given close
// reason, and returns how many were closed.
func (t *Test) DisconnectAll(reason string) int {
	dropped := 0

	for _, id := range t.connectionIDs() {
		client := t.GetConnection(id)
		if client == nil {
			// Gone between the snapshot and here, which is the same outcome.
			continue
		}

		client.CloseWithReason(websocket.CloseNormalClosure, reason)
		t.RemoveConnection(id)

		dropped++
	}

	return dropped
}
