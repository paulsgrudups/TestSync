package ws

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/paulsgrudups/testsync/internal/app"
	"github.com/paulsgrudups/testsync/internal/apptest"
)

// TestBurstOfCommandsDuringARelease is the CONC-2 regression test at the
// server level.
//
// A checkpoint release is written to every member by whichever agent's
// goroutine completed the barrier, while those same agents are sending
// commands of their own. gorilla/websocket permits one writer per connection
// and panics when that is violated, so the release and the replies really do
// contend. Every write goes through the connection's own writer, so the panic
// cannot happen — this proves the barrier still releases correctly while it is
// happening.
func TestBurstOfCommandsDuringARelease(t *testing.T) {
	t.Parallel()

	const (
		testID = 800
		agents = 4
		burst  = 25
	)

	server, _ := newIntegrationServer(t)

	joined := make([]*agent, 0, agents)
	for range agents {
		joined = append(joined, newAgent(t, server, testID))
	}

	// Everyone but the last joins, so the barrier is one agent short and the
	// release is guaranteed to happen while the burst is in flight.
	for _, a := range joined[:agents-1] {
		a.waitCheckpoint("burst-barrier", agents)
	}

	var wg sync.WaitGroup

	// Each waiting agent hammers the server with commands that write back to
	// it. One goroutine per connection: gorilla/websocket allows a single
	// writer per connection on the client side too, so the test must respect
	// the rule it is checking the server for.
	for _, a := range joined[:agents-1] {
		wg.Go(func() {
			for range burst {
				a.send(CommandGetConnectionCount, map[string]string{})
			}
		})
	}

	// ... while the last arrival releases the barrier in the middle of it.
	wg.Go(func() {
		joined[agents-1].send(CommandWaitCheckpoint, map[string]any{
			"identifier":   "burst-barrier",
			"target_count": agents,
		})
	})

	wg.Wait()

	// Every agent is released exactly once, and with the right reason: the
	// burst neither lost a release nor produced a second one. The release is
	// mixed in among the burst's own replies, so it has to be picked out.
	for i, a := range joined {
		release, extra := findRelease(t, a, 10*time.Second)

		if release.Reason != "complete" {
			t.Fatalf("agent %d released with reason %q", i, release.Reason)
		}

		if release.Joined != agents || release.Target != agents {
			t.Fatalf("agent %d saw %d/%d, expected %d/%d",
				i, release.Joined, release.Target, agents, agents)
		}

		if extra > 0 {
			t.Fatalf("agent %d was released %d extra times", i, extra)
		}
	}
}

// findRelease drains an agent's messages until it finds a checkpoint release,
// then keeps draining briefly to prove no second one arrives. Everything else
// on the wire is a reply to the burst and is ignored.
func findRelease(t *testing.T, a *agent, timeout time.Duration) (checkpointRelease, int) {
	t.Helper()

	var (
		release checkpointRelease
		found   bool
		extra   int
	)

	deadline := time.After(timeout)

	for {
		select {
		case m := <-a.messages:
			if m.Command != CommandWaitCheckpoint {
				continue
			}

			if found {
				extra++
				continue
			}

			if err := json.Unmarshal(m.Content.Bytes, &release); err != nil {
				t.Fatalf("could not parse a release: %v", err)
			}

			found = true

			// Keep reading for a moment: a duplicate release is exactly the
			// kind of thing a racing broadcast would produce.
			deadline = time.After(500 * time.Millisecond)
		case <-deadline:
			if !found {
				t.Fatal("the agent was never released")
			}

			return release, extra
		}
	}
}

// TestFrozenPeerDoesNotStallTheRun is the server-level half of CONC-9.
//
// An agent that stops reading its socket must not take the run with it. Two
// things protect against that, and they fire at different times: a peer whose
// outbound queue overflows is dropped immediately, and a peer whose socket has
// stopped accepting writes is reclaimed when the writer's 10 s write deadline
// expires. Measured against this server, the write deadline is what fires
// first, so that is what the reclamation assertion below is bounded by.
//
// The queue-overflow half is not observable from here — the write deadline
// reclaims the connection either way — so it is guarded directly in
// wsutil.TestSendClosesAClientThatCannotKeepUp, which deadlocks if Send is
// made to block.
func TestFrozenPeerDoesNotStallTheRun(t *testing.T) {
	t.Parallel()

	const (
		testID   = 801
		agents   = 3
		payload  = 1 << 20 // 1 MiB per reply
		requests = 300     // far more than the client's outbound buffer
	)

	server, application := newIntegrationServer(t)

	// Seed a large payload for the frozen peer to keep asking for. Small
	// replies vanish into the kernel's socket buffer and never wedge anything.
	if err := application.Service.UpdateTestData(
		t.Context(), testID, make([]byte, payload),
	); err != nil {
		t.Fatalf("failed to seed the payload: %v", err)
	}

	// The frozen peer: a raw connection that nothing ever reads from.
	frozen := dialRaw(t, server, fmt.Sprintf("/register/%d", testID))

	readers := make([]*agent, 0, agents-1)
	for range agents - 1 {
		readers = append(readers, newAgent(t, server, testID))
	}

	waitForConnections(t, application, testID, agents)

	for range requests {
		if err := writeWS(frozen, CommandReadData, map[string]string{}); err != nil {
			break
		}
	}

	// The agents that are still reading complete a barrier between them while
	// the third is wedged. This is what an operator cares about: one stuck
	// agent must not stop the rest of the suite.
	for _, a := range readers {
		a.waitCheckpoint("survivor-barrier", agents-1)
	}

	for i, a := range readers {
		release, ok := a.awaitRelease(10 * time.Second)
		if !ok {
			t.Fatalf("reader %d was never released while a peer was frozen", i)
		}

		if release.Reason != "complete" {
			t.Fatalf("reader %d released with reason %q, expected complete", i, release.Reason)
		}
	}

	if testing.Short() {
		return
	}

	// And the wedged connection is eventually reclaimed rather than held
	// forever. Bounded generously above the 10 s write deadline that does it.
	deadline := time.Now().Add(20 * time.Second)

	for {
		run, ok := application.Registry.Get(testID)
		if !ok || run.ConnectionCount() < agents {
			return
		}

		if time.Now().After(deadline) {
			t.Fatal("a frozen agent was never reclaimed")
		}

		time.Sleep(200 * time.Millisecond)
	}
}

// TestTwoRunsDoNotSeeEachOther covers the isolation agents rely on: barriers
// and connection counts belong to one test ID, so two suites running at once
// cannot release or block each other.
func TestTwoRunsDoNotSeeEachOther(t *testing.T) {
	t.Parallel()

	const (
		firstID  = 802
		secondID = 803
	)

	server, _ := newIntegrationServer(t)

	first := newAgent(t, server, firstID)
	second := newAgent(t, server, secondID)

	// Both wait on the same identifier, in different runs, each for two
	// agents. Neither may be released by the other's arrival.
	first.waitCheckpoint("shared-name", 2)
	second.waitCheckpoint("shared-name", 2)

	first.expectSilence(500 * time.Millisecond)
	second.expectSilence(500 * time.Millisecond)

	if count := first.connectionCount(); count != 1 {
		t.Fatalf("run %d sees %d connections, expected 1", firstID, count)
	}

	// A second agent on the first run releases only the first run.
	firstPartner := newAgent(t, server, firstID)
	firstPartner.waitCheckpoint("shared-name", 2)

	if _, ok := first.awaitRelease(10 * time.Second); !ok {
		t.Fatal("the first run was not released by its own second agent")
	}

	second.expectSilence(500 * time.Millisecond)
}

// newIntegrationServer starts a WebSocket server and returns it along with the
// application behind it, so a test can seed stored data or inspect the
// registry directly.
func newIntegrationServer(t *testing.T) (*httptest.Server, *app.App) {
	t.Helper()

	application := apptest.NewInsecure(t)

	httpServer := httptest.NewServer(newWSRouter(newServer(application)))
	t.Cleanup(httpServer.Close)

	return httpServer, application
}
