package runs

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"
)

// hasReason reports whether an error body carries the expected stable reason.
func hasReason(body []byte, want string) bool {
	var resp utils.ErrorResponse

	if err := json.Unmarshal(body, &resp); err != nil {
		return false
	}

	return resp.Reason == want
}

// attach registers the given number of connections on a run and returns their
// IDs. The clients have no socket: nothing in these tests writes to one, and a
// client with no reader of its own is the point — only the code under test can
// remove it from the run.
func attach(t *testing.T, run *Test, count int) []ConnID {
	t.Helper()

	ids := make([]ConnID, 0, count)

	for range count {
		id, err := run.AddConnection(wsutil.NewClient(nil, nil))
		if err != nil {
			t.Fatalf("failed to attach a connection: %v", err)
		}

		ids = append(ids, id)
	}

	return ids
}

// checkpointStateOf finds one barrier in a run's snapshot.
func checkpointStateOf(t *testing.T, run *Test, testID int, identifier string) CheckpointState {
	t.Helper()

	for _, cp := range run.State(testID).Checkpoints {
		if cp.Identifier == identifier {
			return cp
		}
	}

	t.Fatalf("run %d has no checkpoint %q", testID, identifier)

	return CheckpointState{}
}

// TestReleaseCheckpointEndsTheRound covers the force-release: the round in
// progress ends, the agents that were waiting are counted, and the barrier
// moves on to a fresh round on the same identifier.
func TestReleaseCheckpointEndsTheRound(t *testing.T) {
	t.Parallel()

	registry, svc := newTestSetup(t, DefaultLimits())

	run := newRun(t, registry, 1, time.Now())

	ids := attach(t, run, 3)

	for _, id := range ids[:2] {
		if err := run.JoinCheckpoint("stage-1", 3, DefaultCheckpointTimeout, id); err != nil {
			t.Fatalf("failed to join: %v", err)
		}
	}

	result, err := svc.ReleaseCheckpoint(1, "stage-1", "")
	if err != nil {
		t.Fatalf("failed to release: %v", err)
	}

	if result.Reason != ReasonOperatorReleased {
		t.Fatalf("expected the default reason, got %q", result.Reason)
	}

	if result.Generation != 1 || result.Released != 2 || result.Target != 3 {
		t.Fatalf("unexpected release result: %+v", result)
	}

	state := checkpointStateOf(t, run, 1, "stage-1")
	if state.Waiting || state.Generation != 2 {
		t.Fatalf("expected an idle barrier in its second round, got %+v", state)
	}

	// The barrier is reusable, so the next round must work as if the forced
	// one had ended by itself.
	if err := run.JoinCheckpoint("stage-1", 3, DefaultCheckpointTimeout, ids[0]); err != nil {
		t.Fatalf("failed to join the next round: %v", err)
	}

	if next := checkpointStateOf(t, run, 1, "stage-1"); !next.Waiting || next.Generation != 2 {
		t.Fatalf("expected the second round to be waiting, got %+v", next)
	}
}

// TestReleaseCheckpointKeepsTheReason covers an operator-supplied reason
// reaching the release unchanged.
func TestReleaseCheckpointKeepsTheReason(t *testing.T) {
	t.Parallel()

	registry, svc := newTestSetup(t, DefaultLimits())

	run := newRun(t, registry, 2, time.Now())

	id := attach(t, run, 1)[0]

	if err := run.JoinCheckpoint("stage-1", 2, DefaultCheckpointTimeout, id); err != nil {
		t.Fatalf("failed to join: %v", err)
	}

	result, err := svc.ReleaseCheckpoint(2, "stage-1", "  build box died  ")
	if err != nil {
		t.Fatalf("failed to release: %v", err)
	}

	if result.Reason != "build box died" {
		t.Fatalf("expected the trimmed operator reason, got %q", result.Reason)
	}
}

// TestReleaseCheckpointFailures covers each refusal the management API turns
// into a status of its own.
func TestReleaseCheckpointFailures(t *testing.T) {
	t.Parallel()

	registry, svc := newTestSetup(t, DefaultLimits())

	run := newRun(t, registry, 3, time.Now())

	id := attach(t, run, 1)[0]

	// A barrier that exists but is idle: joining a round of one releases it
	// immediately, so the checkpoint is left between rounds.
	if err := run.JoinCheckpoint("done", 1, DefaultCheckpointTimeout, id); err != nil {
		t.Fatalf("failed to join: %v", err)
	}

	tests := map[string]struct {
		testID     int
		identifier string
		want       error
	}{
		"unknown run":        {testID: 99, identifier: "done", want: ErrRunNotFound},
		"unknown checkpoint": {testID: 3, identifier: "never-seen", want: ErrCheckpointNotFound},
		"idle checkpoint":    {testID: 3, identifier: "done", want: ErrNoRoundInProgress},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := svc.ReleaseCheckpoint(
				tc.testID, tc.identifier, "",
			); !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

// TestDisconnectFreesTheBarrierSlot is the regression test for the whole point
// of disconnecting an agent: the slot it held in a round has to be released
// there and then, so that the agents which are left can finish the round
// without it.
//
// The connections here have no reader goroutine, so nothing else can remove
// them: an implementation that closed the socket and left the run to notice
// later would leave the departed agent counted as a member, the third arrival
// would complete a round of the wrong three agents, and the assertions below
// would fail immediately rather than flakily.
func TestDisconnectFreesTheBarrierSlot(t *testing.T) {
	t.Parallel()

	registry, svc := newTestSetup(t, DefaultLimits())

	run := newRun(t, registry, 4, time.Now())

	ids := attach(t, run, 4)

	// The agent that will be disconnected joins first, and so fixes the round
	// at three participants.
	if err := run.JoinCheckpoint("gate", 3, DefaultCheckpointTimeout, ids[0]); err != nil {
		t.Fatalf("failed to join: %v", err)
	}

	if err := svc.DisconnectAgent(4, ids[0]); err != nil {
		t.Fatalf("failed to disconnect: %v", err)
	}

	if run.GetConnection(ids[0]) != nil {
		t.Fatal("the disconnected agent is still registered on the run")
	}

	if count := run.ConnectionCount(); count != 3 {
		t.Fatalf("expected 3 connections left, got %d", count)
	}

	// Two of the three survivors join. That is two members, not three: the
	// disconnected agent must no longer be one of them.
	for _, id := range ids[1:3] {
		if err := run.JoinCheckpoint("gate", 3, DefaultCheckpointTimeout, id); err != nil {
			t.Fatalf("failed to join: %v", err)
		}
	}

	state := checkpointStateOf(t, run, 4, "gate")
	if !state.Waiting || len(state.Members) != 2 || state.Generation != 1 {
		t.Fatalf(
			"expected the first round still waiting with 2 members, got %+v", state,
		)
	}

	// The third survivor completes the round the disconnected agent started.
	if err := run.JoinCheckpoint("gate", 3, DefaultCheckpointTimeout, ids[3]); err != nil {
		t.Fatalf("failed to join: %v", err)
	}

	if done := checkpointStateOf(t, run, 4, "gate"); done.Waiting || done.Generation != 2 {
		t.Fatalf("expected the round to have completed, got %+v", done)
	}
}

// TestDisconnectClosesTheClient covers the other half of a disconnect: the
// agent is told, rather than left holding a socket the server has forgotten
// about.
func TestDisconnectClosesTheClient(t *testing.T) {
	t.Parallel()

	registry, svc := newTestSetup(t, DefaultLimits())

	run := newRun(t, registry, 5, time.Now())

	id := attach(t, run, 1)[0]
	client := run.GetConnection(id)

	if err := svc.DisconnectAgent(5, id); err != nil {
		t.Fatalf("failed to disconnect: %v", err)
	}

	if !client.Closed() {
		t.Fatal("the agent's connection was left open")
	}

	// The same connection cannot be disconnected twice: the ID is gone for
	// good and an operator retrying must be told so.
	if err := svc.DisconnectAgent(5, id); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("expected %v, got %v", ErrConnectionNotFound, err)
	}

	if err := svc.DisconnectAgent(404, id); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("expected %v, got %v", ErrRunNotFound, err)
	}
}

// TestDeleteRunRemovesBothHalves covers the run and its stored payload going
// away together, and the attached agents being counted and closed.
func TestDeleteRunRemovesBothHalves(t *testing.T) {
	t.Parallel()

	registry, svc := newTestSetup(t, DefaultLimits())

	if err := svc.UpdateTestData(t.Context(), 6, []byte("payload")); err != nil {
		t.Fatalf("failed to store data: %v", err)
	}

	run, ok := registry.Get(6)
	if !ok {
		t.Fatal("storing data did not register the run")
	}

	ids := attach(t, run, 2)
	client := run.GetConnection(ids[0])

	dropped, err := svc.DeleteRun(t.Context(), 6)
	if err != nil {
		t.Fatalf("failed to delete the run: %v", err)
	}

	if dropped != 2 {
		t.Fatalf("expected 2 dropped connections, got %d", dropped)
	}

	if !client.Closed() {
		t.Fatal("an agent of the deleted run was left connected")
	}

	if _, ok := registry.Get(6); ok {
		t.Fatal("the run is still registered")
	}

	if _, err := svc.ReadTestData(t.Context(), 6); !errors.Is(err, ErrTestNotFound) {
		t.Fatalf("expected the payload to be gone, got %v", err)
	}

	if _, err := svc.DeleteRun(t.Context(), 6); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("expected %v on a second delete, got %v", ErrRunNotFound, err)
	}
}

// TestDeleteRunWithoutARegisteredRun covers a payload that outlived its run,
// which is what a restart leaves behind: the runs live in memory and the
// payloads do not.
func TestDeleteRunWithoutARegisteredRun(t *testing.T) {
	t.Parallel()

	registry, svc := newTestSetup(t, DefaultLimits())

	if err := svc.UpdateTestData(t.Context(), 7, []byte("payload")); err != nil {
		t.Fatalf("failed to store data: %v", err)
	}

	registry.Delete(7)

	dropped, err := svc.DeleteRun(t.Context(), 7)
	if err != nil {
		t.Fatalf("failed to delete the stored payload: %v", err)
	}

	if dropped != 0 {
		t.Fatalf("expected no dropped connections, got %d", dropped)
	}

	if _, err := svc.ReadTestData(t.Context(), 7); !errors.Is(err, ErrTestNotFound) {
		t.Fatalf("expected the payload to be gone, got %v", err)
	}
}

// TestDeleteRunHandler covers the shared handler both DELETE routes use: 204
// with the dropped-connection count, and 404 for a run that never existed.
func TestDeleteRunHandler(t *testing.T) {
	t.Parallel()

	registry, svc := newTestSetup(t, DefaultLimits())

	if err := svc.UpdateTestData(t.Context(), 8, []byte("payload")); err != nil {
		t.Fatalf("failed to store data: %v", err)
	}

	run, ok := registry.Get(8)
	if !ok {
		t.Fatal("storing data did not register the run")
	}

	attach(t, run, 3)

	router := mux.NewRouter()
	router.HandleFunc(`/tests/{testID:\d+}`, svc.DeleteRunHandler).
		Methods(http.MethodDelete)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/tests/8", nil))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}

	if rec.Body.Len() != 0 {
		t.Fatalf("expected no body, got %q", rec.Body.String())
	}

	if got := rec.Header().Get(ConnectionsDroppedHeader); got != "3" {
		t.Fatalf("expected 3 dropped connections in the header, got %q", got)
	}

	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest(http.MethodDelete, "/tests/9999", nil))

	if missing.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, missing.Code)
	}

	if !hasReason(missing.Body.Bytes(), ErrorReasonRunNotFound) {
		t.Fatalf("expected reason %q, got %s", ErrorReasonRunNotFound, missing.Body.String())
	}
}
