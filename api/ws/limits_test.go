package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/internal/storagetest"
	"github.com/paulsgrudups/testsync/wsutil"
)

// withLimits installs limits for one test and restores them afterwards.
func withLimits(t *testing.T, limits runs.Limits) {
	t.Helper()

	previous := runs.CurrentLimits()
	t.Cleanup(func() { runs.SetLimits(previous) })

	runs.SetLimits(limits)
}

// readRejection reads the next message and decodes it as an "error" reply.
func readRejection(t *testing.T, conn *websocket.Conn) ErrorContent {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}

	_, body, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected an error reply, got: %v", err)
	}

	var message wsutil.Message
	if err := json.Unmarshal(body, &message); err != nil {
		t.Fatalf("failed to unmarshal reply %q: %v", string(body), err)
	}

	if message.Command != CommandError {
		t.Fatalf("expected an %q reply, got %q", CommandError, message.Command)
	}

	var content ErrorContent
	if err := json.Unmarshal(message.Content.Bytes, &content); err != nil {
		t.Fatalf("failed to unmarshal error content: %v", err)
	}

	return content
}

// TestConnectionLimitClosesWithTryAgainLater covers the documented rejection
// for limits.max_connections_per_test: close code 1013, so an agent knows it
// was turned away and may retry, rather than seeing an abnormal closure it
// cannot tell from a crash.
func TestConnectionLimitClosesWithTryAgainLater(t *testing.T) {
	const testID = 40

	limits := runs.DefaultLimits()
	limits.MaxConnectionsPerTest = 1
	withLimits(t, limits)

	runs.SetDataStore(storagetest.NewStore(t))

	server := newLifecycleServer(t, &Server{Handler: NewCommandHandler(nil)}, testID)
	path := fmt.Sprintf("/register/%d", testID)

	first := dialRaw(t, server, path)

	// The first agent has to be registered before the second dials, otherwise
	// the second may legitimately take the only slot.
	waitForConnections(t, testID, 1)

	second := dialRaw(t, server, path)

	if err := second.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}

	_, _, err := second.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseTryAgainLater) {
		t.Fatalf(
			"expected close %d (try again later), got %v",
			websocket.CloseTryAgainLater, err,
		)
	}

	// The rejection cost the accepted agent nothing.
	if err := writeWS(first, CommandGetConnectionCount, map[string]string{}); err != nil {
		t.Fatalf("the accepted agent stopped working: %v", err)
	}

	if err := first.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}

	if _, _, err := first.ReadMessage(); err != nil {
		t.Fatalf("the accepted agent stopped working: %v", err)
	}
}

// TestCheckpointLimitReportsRejection covers the documented rejection for
// limits.max_checkpoints_per_test: an "error" reply naming the limit, on the
// connection that asked for it, with the connection left usable.
func TestCheckpointLimitReportsRejection(t *testing.T) {
	const testID = 41

	limits := runs.DefaultLimits()
	limits.MaxCheckpointsPerTest = 1
	withLimits(t, limits)

	runs.SetDataStore(storagetest.NewStore(t))

	server := newLifecycleServer(t, &Server{Handler: NewCommandHandler(nil)}, testID)
	conn := dialRaw(t, server, fmt.Sprintf("/register/%d", testID))

	join := func(identifier string) error {
		return writeWS(conn, CommandWaitCheckpoint, map[string]any{
			"identifier":   identifier,
			"target_count": 2,
		})
	}

	if err := join("round-1"); err != nil {
		t.Fatalf("wait_checkpoint failed: %v", err)
	}

	if err := join("round-2"); err != nil {
		t.Fatalf("wait_checkpoint failed: %v", err)
	}

	content := readRejection(t, conn)
	if content.Code != CodeCheckpointLimitReached {
		t.Fatalf("expected %q, got %+v", CodeCheckpointLimitReached, content)
	}

	// The connection is still usable: a limit is a refused command, not a
	// broken agent.
	if err := writeWS(conn, CommandGetConnectionCount, map[string]string{}); err != nil {
		t.Fatalf("the connection was closed by the rejection: %v", err)
	}

	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("the connection was closed by the rejection: %v", err)
	}
}

// TestUpdateDataRejectsOversizedPayload covers the documented rejection for
// limits.max_data_bytes on the WebSocket path.
func TestUpdateDataRejectsOversizedPayload(t *testing.T) {
	const testID = 42

	limits := runs.DefaultLimits()
	limits.MaxDataBytes = 64
	withLimits(t, limits)

	runs.SetDataStore(storagetest.NewStore(t))

	server := newLifecycleServer(t, &Server{Handler: NewCommandHandler(nil)}, testID)
	conn := dialRaw(t, server, fmt.Sprintf("/register/%d", testID))

	oversized := map[string]string{"data": string(make([]byte, 128))}
	if err := writeWS(conn, CommandUpdateData, oversized); err != nil {
		t.Fatalf("update_data failed: %v", err)
	}

	content := readRejection(t, conn)
	if content.Code != CodePayloadTooLarge {
		t.Fatalf("expected %q, got %+v", CodePayloadTooLarge, content)
	}

	if _, err := runs.NewService(nil).ReadTestData(testID); err == nil {
		t.Fatal("a refused payload was stored")
	}
}

// TestShutdownClosesAgentsWithServiceRestart covers STAB-6 on the wire: on
// shutdown an agent is told the server is going away with close code 1012, so
// a deploy is distinguishable from a crash. [http.Server.Shutdown] does not
// track hijacked connections, so nothing used to reach the agents at all.
func TestShutdownClosesAgentsWithServiceRestart(t *testing.T) {
	const testID = 43

	runs.SetDataStore(storagetest.NewStore(t))

	server := newLifecycleServer(t, &Server{Handler: NewCommandHandler(nil)}, testID)
	conn := dialRaw(t, server, fmt.Sprintf("/register/%d", testID))

	waitForConnections(t, testID, 1)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if closed := runs.CloseAllConnections(ctx, websocket.CloseServiceRestart, "bye"); closed != 1 {
		t.Fatalf("expected 1 connection to be closed, got %d", closed)
	}

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}

	_, _, err := conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseServiceRestart) {
		t.Fatalf(
			"expected close %d (service restart), got %v",
			websocket.CloseServiceRestart, err,
		)
	}
}

// waitForConnections polls until a run has the expected number of agents.
func waitForConnections(t *testing.T, testID, want int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for {
		run, ok := runs.GetTest(testID)
		if ok && run.ConnectionCount() == want {
			return
		}

		if time.Now().After(deadline) {
			count := 0
			if ok {
				count = run.ConnectionCount()
			}

			t.Fatalf("expected %d connections on test %d, got %d", want, testID, count)
		}

		time.Sleep(10 * time.Millisecond)
	}
}
