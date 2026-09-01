// Package ws contains WebSocket server tests.
package ws

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"

	"github.com/paulsgrudups/testsync/api/runs"
)

// newLifecycleServer starts a WebSocket server with no connection history for
// the given test ID.
func newLifecycleServer(t *testing.T, s *Server, testID int) *httptest.Server {
	t.Helper()

	runs.DeleteTest(testID)

	httpServer := httptest.NewServer(newWSRouter(s))
	t.Cleanup(httpServer.Close)

	return httpServer
}

// dialRaw opens a WebSocket connection without the message pump newAgent
// starts: these tests need to control exactly when the peer reads.
func dialRaw(t *testing.T, server *httptest.Server, path string) *websocket.Conn {
	t.Helper()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + path

	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("failed to dial %s: %v", url, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// openFileDescriptors returns the number of descriptors held by this process,
// or -1 where the platform does not expose them.
func openFileDescriptors() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}

	return len(entries)
}

// waitForFileDescriptors polls until the descriptor count drops to limit.
func waitForFileDescriptors(t *testing.T, limit int, timeout time.Duration) {
	t.Helper()

	if limit < 0 {
		return
	}

	deadline := time.Now().Add(timeout)

	for {
		count := openFileDescriptors()
		if count <= limit {
			return
		}

		if time.Now().After(deadline) {
			t.Errorf(
				"file descriptor count did not return to baseline: %d, want <= %d",
				count, limit,
			)

			return
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// TestRegisterRejectsUnusableTestID is the CONC-11 regression test.
//
// The route pattern accepted any number of digits and the handler upgraded the
// connection before parsing the ID, so an ID that does not fit in an int left
// an upgraded socket with no reader behind, and wrote its HTTP error to an
// already hijacked ResponseWriter.
func TestRegisterRejectsUnusableTestID(t *testing.T) {
	const (
		attempts   = 50
		tooLongID  = "99999999999999999999" // 20 digits: cannot be routed
		overflowID = "9999999999999999999"  // 19 digits: routable, but overflows
	)

	server := newLifecycleServer(t, &Server{Handler: NewCommandHandler(nil)}, 0)

	baselineGoroutines := runtime.NumGoroutine()
	baselineFDs := openFileDescriptors()

	for _, id := range []string{tooLongID, overflowID} {
		url := "ws" + strings.TrimPrefix(server.URL, "http") + "/register/" + id

		for i := range attempts {
			conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
			if err == nil {
				_ = conn.Close()
				t.Fatalf(
					"attempt %d: handshake succeeded for unusable test ID %s",
					i, id,
				)
			}

			if resp == nil {
				t.Fatalf("attempt %d: no HTTP response for %s: %v", i, id, err)
			}
			_ = resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest &&
				resp.StatusCode != http.StatusNotFound {
				t.Fatalf(
					"attempt %d: unexpected status for %s: %d",
					i, id, resp.StatusCode,
				)
			}
		}
	}

	waitForGoroutines(t, baselineGoroutines+2, 5*time.Second)
	waitForFileDescriptors(t, baselineFDs+2, 5*time.Second)
}

// TestReaderReapsUnresponsivePeer is the CONC-10 regression test.
//
// The reader had no read deadline and no pong handler, so a peer that stopped
// answering keepalive pings held its reader goroutine, its file descriptor and
// its slot in the test's connection list forever.
func TestReaderReapsUnresponsivePeer(t *testing.T) {
	const testID = 11

	// A short pong wait keeps the test quick; the production default is
	// wsutil.PongWait, three ping periods.
	server := newLifecycleServer(
		t,
		&Server{Handler: NewCommandHandler(nil), pongWait: 500 * time.Millisecond},
		testID,
	)

	baseline := runtime.NumGoroutine()

	conn := dialRaw(t, server, fmt.Sprintf("/register/%d", testID))

	// A peer that still receives frames but never answers a ping.
	conn.SetPingHandler(func(string) error { return nil })

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}

	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected the server to close the unresponsive connection")
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("server did not reap the unresponsive peer: %v", err)
	}

	waitForGoroutines(t, baseline+2, 5*time.Second)
}

// TestPongExtendsReadDeadline is the other half of CONC-10: the read deadline
// must reap only peers that have actually gone away. A peer that answers the
// keepalive keeps its connection for as long as it likes.
func TestPongExtendsReadDeadline(t *testing.T) {
	const (
		testID   = 15
		pongWait = 300 * time.Millisecond
		pongs    = 5
	)

	server := newLifecycleServer(
		t,
		&Server{Handler: NewCommandHandler(nil), pongWait: pongWait},
		testID,
	)

	conn := dialRaw(t, server, fmt.Sprintf("/register/%d", testID))

	// Five pong periods: without the pong handler extending the deadline the
	// connection would have been reaped four times over.
	for range pongs {
		time.Sleep(pongWait / 3)

		err := conn.WriteControl(
			websocket.PongMessage, nil, time.Now().Add(time.Second),
		)
		if err != nil {
			t.Fatalf("failed to send pong: %v", err)
		}
	}

	assertServesConnectionCount(t, conn)
}

// TestReaderRejectsOversizedFrame is the STAB-2 regression test.
//
// Only the frame header is written: with no read limit the server waits for
// (and allocates) the declared payload, so a client can name any size it likes.
func TestReaderRejectsOversizedFrame(t *testing.T) {
	const (
		testID      = 12
		declaredLen = uint64(64 << 20)
	)

	server := newLifecycleServer(
		t,
		&Server{Handler: NewCommandHandler(nil)},
		testID,
	)

	conn := dialRaw(t, server, fmt.Sprintf("/register/%d", testID))

	// FIN + text frame, masked, 64-bit length, masking key. No payload
	// follows: the server must reject the frame from its header alone.
	header := []byte{0x81, 0x80 | 127}
	header = binary.BigEndian.AppendUint64(header, declaredLen)
	header = append(header, 0xAA, 0xBB, 0xCC, 0xDD)

	if _, err := conn.UnderlyingConn().Write(header); err != nil {
		t.Fatalf("failed to write frame header: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}

	_, _, err := conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Fatalf(
			"expected close %d (message too big), got %v",
			websocket.CloseMessageTooBig, err,
		)
	}
}

// panickingStore is a data store that panics on the read path, standing in for
// any bug that panics inside a command handler.
type panickingStore struct{}

func (panickingStore) SaveData(_ int, _ []byte) error { return nil }

func (panickingStore) LoadData(_ int) ([]byte, bool, error) {
	panic("boom: injected panic in a command handler")
}

func (panickingStore) DeleteData(_ int) error { return nil }

func (panickingStore) DeleteOlderThanExcept(_ time.Time, _ []int) error { return nil }

func (panickingStore) Close() error { return nil }

// TestPanicInHandlerKillsOnlyOneConnection is the STAB-1 regression test.
//
// Reader goroutines had no recover, so a panic in one connection's handler
// terminated the process and every other agent's run with it.
func TestPanicInHandlerKillsOnlyOneConnection(t *testing.T) {
	const testID = 13

	server := newLifecycleServer(
		t,
		&Server{Handler: NewCommandHandler(runs.NewService(panickingStore{}))},
		testID,
	)

	path := fmt.Sprintf("/register/%d", testID)
	victim := dialRaw(t, server, path)
	bystander := dialRaw(t, server, path)

	if err := writeWS(victim, CommandReadData, map[string]string{}); err != nil {
		t.Fatalf("read_data failed: %v", err)
	}

	// The panicking connection, and only it, is closed.
	if err := victim.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}
	if _, _, err := victim.ReadMessage(); err == nil {
		t.Fatal("expected the panicking connection to be closed")
	}

	assertServesConnectionCount(t, bystander)

	// The server still accepts new connections afterwards.
	assertServesConnectionCount(t, dialRaw(t, server, path))
}

// assertServesConnectionCount asserts the connection still gets an answer.
func assertServesConnectionCount(t *testing.T, conn *websocket.Conn) {
	t.Helper()

	if err := writeWS(conn, CommandGetConnectionCount, map[string]string{}); err != nil {
		t.Fatalf("get_connection_count failed: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}

	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("server stopped serving the connection: %v", err)
	}
}

// TestWSRouterRecoversHandlerPanic covers the WebSocket router's half of
// STAB-1: a panic in a route handler is logged and answered with a 500 rather
// than unwinding into the caller.
func TestWSRouterRecoversHandlerPanic(t *testing.T) {
	router, ok := newWSRouter(&Server{Handler: NewCommandHandler(nil)}).(*mux.Router)
	if !ok {
		t.Fatal("expected newWSRouter to return a *mux.Router")
	}

	router.HandleFunc("/panic", func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}

// TestConcurrentRegistrationsDoNotRaceOnUpgrader is the CONC-3 regression test.
//
// registerWS used to assign upgrader.CheckOrigin on every request, writing to a
// package-level struct that concurrent Upgrade calls were reading at the same
// time. Run with -race, simultaneous registrations report that write.
func TestConcurrentRegistrationsDoNotRaceOnUpgrader(t *testing.T) {
	const (
		testID      = 14
		connections = 16
	)

	server := newLifecycleServer(
		t,
		&Server{Handler: NewCommandHandler(nil)},
		testID,
	)

	url := fmt.Sprintf(
		"ws%s/register/%d", strings.TrimPrefix(server.URL, "http"), testID,
	)

	start := make(chan struct{})

	var wg sync.WaitGroup
	for range connections {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if err != nil {
				t.Errorf("failed to dial %s: %v", url, err)
				return
			}

			if err := conn.Close(); err != nil {
				t.Errorf("failed to close connection: %v", err)
			}
		}()
	}

	close(start)
	wg.Wait()
}
