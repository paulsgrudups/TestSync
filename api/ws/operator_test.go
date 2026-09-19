package ws

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"

	"github.com/paulsgrudups/testsync/api/monitor"
	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/internal/app"
	"github.com/paulsgrudups/testsync/internal/apptest"
)

// newOperatorServers starts a WebSocket server and the management API over one
// application, which is how they run in production: an operator acts on the
// very runs the connected agents are attached to. Authentication is disabled
// here — these tests are about what an override does to a live barrier, and
// the credentials on both surfaces have tests of their own.
func newOperatorServers(t *testing.T) (*httptest.Server, *httptest.Server, *app.App) {
	t.Helper()

	application := apptest.NewInsecure(t)

	wsServer := httptest.NewServer(newWSRouter(newServer(application)))
	t.Cleanup(wsServer.Close)

	router := mux.NewRouter().StrictSlash(false)
	monitor.RegisterRoutes(router, application)

	apiServer := httptest.NewServer(router)
	t.Cleanup(apiServer.Close)

	return wsServer, apiServer, application
}

// operatorRequest performs one management API call and returns its status and
// body.
func operatorRequest(
	t *testing.T, server *httptest.Server, method, path, body string,
) (int, []byte) {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, reader)
	if err != nil {
		t.Fatalf("failed to build the %s %s request: %v", method, path, err)
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("failed to perform %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read the %s %s response: %v", method, path, err)
	}

	return resp.StatusCode, answer
}

// TestOperatorReleaseUnsticksAWaitingRound is the end-to-end regression test
// for a forced release, over real sockets.
//
// Three agents are wedged on a barrier that is waiting for a fourth which is
// never coming. The operator releases the round, and every waiting agent must
// be told, in the existing envelope, that it may resume — and that the barrier
// was NOT met.
//
// finished: false is the assertion that matters. An implementation that ended
// the round as [runs.ReasonComplete], or that set finished for any release,
// would tell three agents that a four-agent barrier had synchronized, which is
// the one thing a checkpoint exists to promise. The value is derived from the
// reason, so this fails the moment the reason is wrong too.
func TestOperatorReleaseUnsticksAWaitingRound(t *testing.T) {
	t.Parallel()

	const (
		testID  = 910
		waiting = 3
		target  = 4
	)

	wsServer, apiServer, application := newOperatorServers(t)

	joined := make([]*agent, 0, waiting)
	for range waiting {
		joined = append(joined, newAgent(t, wsServer, testID))
	}

	waitForConnections(t, application, testID, waiting)

	for _, a := range joined {
		a.waitCheckpoint("wedged", target)
		// The join is handled before the next command from this connection,
		// so this proves the agent really is on the barrier before the
		// operator releases it.
		a.awaitProcessed()
	}

	before := time.Now()

	status, body := operatorRequest(t, apiServer, http.MethodPost,
		fmt.Sprintf("/api/v1/runs/%d/checkpoints/release", testID),
		`{"identifier":"wedged"}`,
	)

	if status != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, status, body)
	}

	var result struct {
		Identifier string `json:"identifier"`
		Reason     string `json:"reason"`
		Generation int    `json:"generation"`
		Released   int    `json:"released"`
		Target     int    `json:"target"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to decode %q: %v", body, err)
	}

	if result.Released != waiting || result.Target != target || result.Generation != 1 {
		t.Fatalf("unexpected release result: %+v", result)
	}

	for i, a := range joined {
		release, ok := a.awaitRelease(5 * time.Second)
		if !ok {
			t.Fatalf("agent %d was never released", i)
		}

		if release.Finished {
			t.Fatalf(
				"agent %d was told a barrier of %d completed with %d agents: %+v",
				i, target, waiting, release,
			)
		}

		if release.Reason != runs.ReasonOperatorReleased {
			t.Fatalf("agent %d released with reason %q", i, release.Reason)
		}

		if release.Identifier != "wedged" || release.Joined != waiting || release.Target != target {
			t.Fatalf("agent %d got an inconsistent release: %+v", i, release)
		}

		if release.Generation != 1 {
			t.Fatalf("agent %d was released from round %d", i, release.Generation)
		}

		// The shared resume moment is what makes this a release and not just
		// a wake-up: every agent must get one, and it must be in the future.
		if release.StartAt < before.UnixMilli() {
			t.Fatalf("agent %d got start_at %d, before the release itself", i, release.StartAt)
		}
	}

	// The barrier is reusable, and a forced release must leave it that way:
	// the same identifier runs its next round, and that one completes for
	// real.
	for _, a := range joined {
		a.waitCheckpoint("wedged", waiting)
	}

	for i, a := range joined {
		release, ok := a.awaitRelease(5 * time.Second)
		if !ok {
			t.Fatalf("agent %d was not released by the next round", i)
		}

		if !release.Finished || release.Reason != runs.ReasonComplete {
			t.Fatalf("agent %d did not complete the next round: %+v", i, release)
		}

		if release.Generation != 2 {
			t.Fatalf("agent %d completed round %d, expected the second", i, release.Generation)
		}
	}
}

// TestOperatorDisconnectClosesTheSocket covers what the disconnected agent
// itself sees: a normal closure carrying the reason, rather than a socket that
// simply dies. An agent that is told can report "an operator dropped me"
// instead of "the server crashed".
func TestOperatorDisconnectClosesTheSocket(t *testing.T) {
	t.Parallel()

	const testID = 911

	wsServer, apiServer, application := newOperatorServers(t)

	// A raw connection, because this test needs the close error itself, and
	// the agent helper's reader swallows it.
	conn := dialRaw(t, wsServer, fmt.Sprintf("/register/%d", testID))

	waitForConnections(t, application, testID, 1)

	run, ok := application.Registry.Get(testID)
	if !ok {
		t.Fatal("the run was never registered")
	}

	connID := run.State(testID).Connections[0].ID

	status, body := operatorRequest(t, apiServer, http.MethodDelete,
		fmt.Sprintf("/api/v1/runs/%d/connections/%d", testID, connID), "",
	)

	if status != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, status, body)
	}

	// The run stops counting the agent as the request returns, not whenever
	// the socket happens to finish unwinding.
	if count := run.ConnectionCount(); count != 0 {
		t.Fatalf("expected the agent to be gone, %d still attached", count)
	}

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set a read deadline: %v", err)
	}

	_, _, err := conn.ReadMessage()

	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("expected a close frame, got %v", err)
	}

	if closeErr.Code != websocket.CloseNormalClosure {
		t.Fatalf("expected close code %d, got %d", websocket.CloseNormalClosure, closeErr.Code)
	}

	if closeErr.Text != runs.OperatorDisconnectReason {
		t.Fatalf("expected close reason %q, got %q", runs.OperatorDisconnectReason, closeErr.Text)
	}
}
