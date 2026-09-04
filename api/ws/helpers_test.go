package ws

import (
	"testing"
	"time"

	"github.com/paulsgrudups/testsync/internal/app"
	"github.com/paulsgrudups/testsync/internal/apptest"
)

// newServer builds a WebSocket server over the given application. The
// registry, the service and the validator all ride on the App, so one test's
// server shares nothing with another's (CODE-1, TEST-2).
func newServer(a *app.App) *Server {
	return &Server{Handler: NewCommandHandler(a.Service), app: a}
}

// newInsecureServer builds a server with authentication deliberately
// disabled, which is what most of these tests want: they are about the
// connection lifecycle, not about credentials.
func newInsecureServer(t *testing.T) *Server {
	t.Helper()

	return newServer(apptest.NewInsecure(t))
}

// waitForConnections polls until a run has the expected number of agents.
func waitForConnections(t *testing.T, a *app.App, testID, want int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for {
		run, ok := a.Registry.Get(testID)
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
