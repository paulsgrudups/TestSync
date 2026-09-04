package ws

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/paulsgrudups/testsync/wsutil"

	log "github.com/sirupsen/logrus"
)

// TestMain silences the global logger: these tests open dozens of connections
// and are meant to be run with -count=20, which makes the per-message logging
// unreadable.
//
// Authentication is no longer set up here. Each server carries its own
// validator, so a test that wants an open server asks for one (SEC-1, CODE-1).
func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)

	os.Exit(m.Run())
}

// checkpointRelease is the payload every participant receives when a
// checkpoint round ends. The field names are part of the wire protocol:
// identifier, finished and start_at are the original three, the rest were
// added for CONC-6 and CONC-8.
type checkpointRelease struct {
	Identifier string `json:"identifier"`
	Finished   bool   `json:"finished"`
	StartAt    int64  `json:"start_at"`
	Reason     string `json:"reason"`
	Generation int    `json:"generation"`
	Joined     int    `json:"joined"`
	Target     int    `json:"target"`
}

// newCheckpointTestServer starts a WebSocket server over a registry of its
// own, so it begins with no checkpoint history whatever ran before it.
func newCheckpointTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	server := newInsecureServer(t)

	httpServer := httptest.NewServer(newWSRouter(server))
	t.Cleanup(httpServer.Close)

	return httpServer
}

// agent is a test client for a single WebSocket connection. Incoming messages
// are drained by a dedicated goroutine so that a test can assert that a message
// does *not* arrive: an expired read deadline is a sticky error in
// gorilla/websocket and would make the connection unusable afterwards.
type agent struct {
	t         *testing.T
	conn      *websocket.Conn
	messages  chan wsutil.Message
	done      chan struct{}
	closeOnce sync.Once
}

func newAgent(t *testing.T, server *httptest.Server, testID int) *agent {
	t.Helper()

	url := fmt.Sprintf(
		"ws%s/register/%d", strings.TrimPrefix(server.URL, "http"), testID,
	)

	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("failed to dial %s: %v", url, err)
	}

	a := &agent{
		t:        t,
		conn:     conn,
		messages: make(chan wsutil.Message, 16),
		done:     make(chan struct{}),
	}

	go a.readLoop()
	t.Cleanup(a.close)

	return a
}

func (a *agent) readLoop() {
	for {
		_, body, err := a.conn.ReadMessage()
		if err != nil {
			return
		}

		var m wsutil.Message
		if err := json.Unmarshal(body, &m); err != nil {
			return
		}

		select {
		case a.messages <- m:
		case <-a.done:
			return
		}
	}
}

func (a *agent) close() {
	a.closeOnce.Do(func() {
		close(a.done)
		if err := a.conn.Close(); err != nil {
			// Best-effort close in tests; log for visibility
			log.Debugf("failed to close agent connection: %v", err)
		}
	})
}

func (a *agent) send(command string, content any) {
	if err := writeWS(a.conn, command, content); err != nil {
		a.t.Errorf("failed to send %s: %v", command, err)
	}
}

func (a *agent) waitCheckpoint(identifier string, target int) {
	a.send(CommandWaitCheckpoint, map[string]any{
		"identifier":   identifier,
		"target_count": target,
	})
}

// waitCheckpointWithin joins a checkpoint with an explicit round timeout.
func (a *agent) waitCheckpointWithin(identifier string, target int, timeout time.Duration) {
	a.send(CommandWaitCheckpoint, map[string]any{
		"identifier":   identifier,
		"target_count": target,
		"timeout_ms":   timeout.Milliseconds(),
	})
}

// connectionCount asks the server how many connections the test currently
// holds. It returns -1 when the answer does not arrive.
func (a *agent) connectionCount() int {
	a.send(CommandGetConnectionCount, map[string]string{})

	m, ok := a.expect(CommandGetConnectionCount, 5*time.Second)
	if !ok {
		return -1
	}

	var payload struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(m.Content.Bytes, &payload); err != nil {
		a.t.Fatalf("could not parse connection count: %v", err)
	}

	return payload.Count
}

// awaitProcessed round-trips one cheap command, which proves that everything
// this agent sent beforehand has already been handled: a connection's messages
// are read and dispatched in order by its own reader goroutine.
func (a *agent) awaitProcessed() {
	a.connectionCount()
}

// awaitRelease waits for this agent's next checkpoint message and decodes it.
func (a *agent) awaitRelease(timeout time.Duration) (checkpointRelease, bool) {
	a.t.Helper()

	m, ok := a.expect(CommandWaitCheckpoint, timeout)
	if !ok {
		return checkpointRelease{}, false
	}

	var release checkpointRelease
	if err := json.Unmarshal(m.Content.Bytes, &release); err != nil {
		a.t.Errorf("could not parse release: %v", err)
		return checkpointRelease{}, false
	}

	return release, true
}

// connectionCountTimeout bounds how long waitForConnectionCount polls.
const connectionCountTimeout = 5 * time.Second

// waitForConnectionCount polls until the server reports the wanted number of
// connections for the test, or the deadline passes.
func waitForConnectionCount(t *testing.T, a *agent, want int) {
	t.Helper()

	deadline := time.Now().Add(connectionCountTimeout)

	for {
		got := a.connectionCount()
		if got == want {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("connection count did not reach %d, last value %d", want, got)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// expect waits for the next message and asserts its command.
func (a *agent) expect(command string, timeout time.Duration) (wsutil.Message, bool) {
	select {
	case m, ok := <-a.messages:
		if !ok {
			a.t.Errorf("connection closed while waiting for %s", command)
			return wsutil.Message{}, false
		}
		if m.Command != command {
			a.t.Errorf("expected command %s, got %s", command, m.Command)
			return wsutil.Message{}, false
		}

		return m, true
	case <-time.After(timeout):
		a.t.Errorf("timed out after %s waiting for %s", timeout, command)
		return wsutil.Message{}, false
	}
}

// expectSilence asserts that no message arrives within the given window.
func (a *agent) expectSilence(window time.Duration) {
	select {
	case m := <-a.messages:
		a.t.Errorf("expected no message, got %s: %s", m.Command, string(m.Content.Bytes))
	case <-time.After(window):
	}
}

// TestCheckpointReleasesEveryParticipant is the CONC-1 regression test.
//
// Joining a checkpoint used to send on an unbuffered channel that nothing read
// once the barrier had fired, so every participant that raced the release
// wedged its reader goroutine permanently: the connection stayed open at the
// TCP level but never answered another command.
func TestCheckpointReleasesEveryParticipant(t *testing.T) {
	t.Parallel()

	const (
		connections = 32
		targetCount = 16
		testID      = 1
	)

	server := newCheckpointTestServer(t)

	baseline := runtime.NumGoroutine()

	agents := make([]*agent, connections)
	for i := range agents {
		agents[i] = newAgent(t, server, testID)
	}

	var wg sync.WaitGroup
	for i, a := range agents {
		wg.Add(1)

		go func(i int, a *agent) {
			defer wg.Done()

			a.waitCheckpoint("barrier", targetCount)

			m, ok := a.expect(CommandWaitCheckpoint, 10*time.Second)
			if !ok {
				return
			}

			var release checkpointRelease
			if err := json.Unmarshal(m.Content.Bytes, &release); err != nil {
				t.Errorf("connection %d: could not parse release: %v", i, err)
				return
			}
			if release.Identifier != "barrier" || !release.Finished {
				t.Errorf("connection %d: unexpected release: %+v", i, release)
				return
			}

			// The connection must still serve commands after the barrier.
			a.send(CommandGetConnectionCount, map[string]string{})
			a.expect(CommandGetConnectionCount, 10*time.Second)
		}(i, a)
	}

	wg.Wait()

	for _, a := range agents {
		a.close()
	}

	// Every reader, writer and client goroutine must go away with its
	// connection; the old barrier leaked one per wedged participant.
	waitForGoroutines(t, baseline+4, 5*time.Second)
}

// TestCheckpointRejectsRepeatedJoinsFromOneConnection is the CONC-4 regression
// test: members used to be appended to a slice without deduplication, so a
// single connection could satisfy a barrier sized for several agents.
func TestCheckpointRejectsRepeatedJoinsFromOneConnection(t *testing.T) {
	t.Parallel()

	const testID = 2

	server := newCheckpointTestServer(t)

	first := newAgent(t, server, testID)
	for range 5 {
		first.waitCheckpoint("solo", 2)
	}

	// One connection is one member, however many times it joins.
	first.expectSilence(500 * time.Millisecond)

	second := newAgent(t, server, testID)
	second.waitCheckpoint("solo", 2)

	for name, a := range map[string]*agent{"first": first, "second": second} {
		m, ok := a.expect(CommandWaitCheckpoint, 10*time.Second)
		if !ok {
			continue
		}

		var release checkpointRelease
		if err := json.Unmarshal(m.Content.Bytes, &release); err != nil {
			t.Fatalf("%s: could not parse release: %v", name, err)
		}
		if release.Identifier != "solo" || !release.Finished || release.StartAt == 0 {
			t.Fatalf("%s: unexpected release: %+v", name, release)
		}
	}

	// The barrier fires exactly once per participant.
	first.expectSilence(300 * time.Millisecond)
	second.expectSilence(300 * time.Millisecond)
}

// TestCheckpointRejectsInvalidRequests covers the CONC-4 validation: a missing
// or non-positive target count used to release the barrier on the first join.
func TestCheckpointRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	const testID = 3

	server := newCheckpointTestServer(t)

	tests := map[string]map[string]any{
		"missing target count":  {"identifier": "no-target"},
		"zero target count":     {"identifier": "zero", "target_count": 0},
		"negative target count": {"identifier": "negative", "target_count": -1},
		"empty identifier":      {"identifier": "", "target_count": 1},
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			a := newAgent(t, server, testID)
			a.send(CommandWaitCheckpoint, content)
			a.expectSilence(500 * time.Millisecond)
		})
	}
}

// waitForGoroutines polls until the goroutine count drops to limit.
func waitForGoroutines(t *testing.T, limit int, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for {
		count := runtime.NumGoroutine()
		if count <= limit {
			return
		}

		if time.Now().After(deadline) {
			t.Errorf(
				"goroutine count did not return to baseline: %d, want <= %d",
				count, limit,
			)

			return
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// TestConnectionCountDropsAfterDisconnect is the CONC-5 regression test.
//
// A connection's identity used to be its index in an append-only slice, so
// nothing could ever be removed: get_connection_count kept counting agents
// that had gone away, and every agent that sized a barrier from that count
// sized it wrong.
func TestConnectionCountDropsAfterDisconnect(t *testing.T) {
	t.Parallel()

	const testID = 20

	server := newCheckpointTestServer(t)

	first := newAgent(t, server, testID)
	second := newAgent(t, server, testID)

	waitForConnectionCount(t, first, 2)

	// No close handshake: the socket simply goes away, as it does when a CI
	// runner is killed.
	second.close()

	waitForConnectionCount(t, first, 1)
}

// TestCheckpointReleasesSurvivorsWhenParticipantDies is the CONC-6 regression
// test.
//
// A barrier had no participant-loss detection, so an agent that died before
// reaching the checkpoint left every other agent waiting for a count that
// could never be reached, for as long as the CI job lasted.
func TestCheckpointReleasesSurvivorsWhenParticipantDies(t *testing.T) {
	t.Parallel()

	const (
		testID = 21
		target = 3
	)

	server := newCheckpointTestServer(t)

	first := newAgent(t, server, testID)
	second := newAgent(t, server, testID)
	third := newAgent(t, server, testID)

	waitForConnectionCount(t, first, 3)

	// Two of the three join and wait. The third dies without ever arriving.
	first.waitCheckpoint("doomed", target)
	second.waitCheckpoint("doomed", target)
	first.awaitProcessed()
	second.awaitProcessed()

	third.close()

	for name, a := range map[string]*agent{"first": first, "second": second} {
		release, ok := a.awaitRelease(time.Second)
		if !ok {
			continue
		}

		if release.Finished {
			t.Errorf("%s: barrier reported success without every agent: %+v", name, release)
		}
		if release.Reason != "participant_lost" {
			t.Errorf("%s: expected reason participant_lost, got %q", name, release.Reason)
		}
		if release.Joined != 2 || release.Target != target {
			t.Errorf("%s: expected 2 of %d joined, got %+v", name, target, release)
		}
	}
}

// TestCheckpointTimesOutStalledRound is the other half of CONC-6: an agent
// that never arrives, and never disconnects either, must not hold the barrier
// past the round's deadline.
func TestCheckpointTimesOutStalledRound(t *testing.T) {
	t.Parallel()

	const (
		testID = 22
		target = 2
	)

	server := newCheckpointTestServer(t)

	lonely := newAgent(t, server, testID)
	lonely.waitCheckpointWithin("stalled", target, 300*time.Millisecond)

	release, ok := lonely.awaitRelease(5 * time.Second)
	if !ok {
		return
	}

	if release.Finished {
		t.Errorf("barrier reported success with one agent: %+v", release)
	}
	if release.Reason != "timeout" {
		t.Errorf("expected reason timeout, got %q", release.Reason)
	}
	if release.Joined != 1 || release.Target != target {
		t.Errorf("expected 1 of %d joined, got %+v", target, release)
	}
}

// TestCheckpointRoundsAreReusable is the CONC-8 regression test.
//
// A checkpoint identifier used to fire exactly once, after which every join
// was released immediately. A looping suite that reuses an identifier
// therefore desynchronized silently from its second round onwards.
func TestCheckpointRoundsAreReusable(t *testing.T) {
	t.Parallel()

	const (
		testID = 23
		rounds = 3
		target = 2
	)

	server := newCheckpointTestServer(t)

	first := newAgent(t, server, testID)
	second := newAgent(t, server, testID)

	waitForConnectionCount(t, first, 2)

	for round := 1; round <= rounds; round++ {
		first.waitCheckpoint("loop", target)
		first.awaitProcessed()

		// The round must block: one agent of two is not a synchronized run.
		first.expectSilence(300 * time.Millisecond)

		second.waitCheckpoint("loop", target)

		for name, a := range map[string]*agent{"first": first, "second": second} {
			release, ok := a.awaitRelease(5 * time.Second)
			if !ok {
				t.Fatalf("%s: no release in round %d", name, round)
			}

			if !release.Finished || release.Reason != "complete" {
				t.Fatalf("%s: round %d released as %+v", name, round, release)
			}
			if release.Generation != round {
				t.Fatalf("%s: round %d reported generation %d", name, round, release.Generation)
			}
			if release.StartAt == 0 {
				t.Fatalf("%s: round %d has no start_at", name, round)
			}
		}
	}
}
