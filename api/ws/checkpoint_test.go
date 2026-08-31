// Package ws contains WebSocket server tests.
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

	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/wsutil"

	log "github.com/sirupsen/logrus"
)

// TestMain silences the global logger: these tests open dozens of connections
// and are meant to be run with -count=20, which makes the per-message logging
// unreadable.
func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)

	os.Exit(m.Run())
}

// checkpointRelease is the payload every participant receives when a
// checkpoint fires. The field names are part of the wire protocol.
type checkpointRelease struct {
	Identifier string `json:"identifier"`
	Finished   bool   `json:"finished"`
	StartAt    int64  `json:"start_at"`
}

// newCheckpointTestServer starts a WebSocket server for a test ID with no
// checkpoint history. DeleteTest is used rather than replacing runs.AllTests
// because it takes the registry lock (see CONC-12).
func newCheckpointTestServer(t *testing.T, testID int) *httptest.Server {
	t.Helper()

	runs.DeleteTest(testID)

	server := &Server{Handler: NewCommandHandler(nil)}
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
	const (
		connections = 32
		targetCount = 16
		testID      = 1
	)

	server := newCheckpointTestServer(t, testID)

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
	const testID = 2

	server := newCheckpointTestServer(t, testID)

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
	const testID = 3

	server := newCheckpointTestServer(t, testID)

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
