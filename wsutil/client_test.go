package wsutil

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialClient starts a server that wraps its side of one connection in a
// Client, and returns that Client together with the peer's connection.
func dialClient(t *testing.T) (*Client, *websocket.Conn) {
	t.Helper()

	client, peer, start := dialPausedClient(t)
	start()

	return client, peer
}

// dialPausedClient is dialClient with the writer not yet running: start runs
// it. It lets a test put the client in a state before the writer sees any of
// it.
func dialPausedClient(t *testing.T) (*Client, *websocket.Conn, func()) {
	t.Helper()

	ready := make(chan *Client, 1)

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}

			ready <- NewClient(conn, nil)
		},
	))
	t.Cleanup(server.Close)

	peer, resp, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http"), nil,
	)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}

	t.Cleanup(func() { _ = peer.Close() })

	select {
	case client := <-ready:
		return client, peer, func() { go client.WritePump() }
	case <-time.After(5 * time.Second):
		t.Fatal("the server never wrapped the connection")
		return nil, nil, nil
	}
}

// TestClientDeliversMessagesInOrder covers the ordinary path through the one
// goroutine that is allowed to write.
func TestClientDeliversMessagesInOrder(t *testing.T) {
	t.Parallel()

	client, peer := dialClient(t)

	for _, body := range []string{"first", "second", "third"} {
		if err := client.Send(websocket.TextMessage, []byte(body)); err != nil {
			t.Fatalf("failed to send %q: %v", body, err)
		}
	}

	for _, want := range []string{"first", "second", "third"} {
		if err := peer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("failed to set deadline: %v", err)
		}

		_, got, err := peer.ReadMessage()
		if err != nil {
			t.Fatalf("failed to read %q: %v", want, err)
		}

		if string(got) != want {
			t.Fatalf("expected %q, got %q", want, got)
		}
	}
}

// TestConcurrentSendersDoNotRace is the CONC-2 regression test at the level it
// happens. gorilla/websocket panics when two goroutines write to one
// connection, and a checkpoint release is written by whichever agent completed
// the barrier, so several goroutines really do send at once.
func TestConcurrentSendersDoNotRace(t *testing.T) {
	t.Parallel()

	client, peer := dialClient(t)

	// Drain, so the writer never fills its queue and the test measures
	// concurrent sending rather than backpressure.
	received := make(chan int, 1)

	go func() {
		count := 0

		for {
			if err := peer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				break
			}

			if _, _, err := peer.ReadMessage(); err != nil {
				break
			}

			count++
			if count == 50 {
				received <- count
				return
			}
		}

		received <- count
	}()

	var wg sync.WaitGroup

	for range 50 {
		wg.Go(func() {
			_ = client.Send(websocket.TextMessage, []byte("concurrent"))
		})
	}

	wg.Wait()

	select {
	case got := <-received:
		if got != 50 {
			t.Fatalf("expected 50 messages, got %d", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the peer did not receive every message")
	}
}

// TestSendClosesAClientThatCannotKeepUp is the CONC-9 regression test at the
// client level: a peer that has stopped reading must be dropped rather than
// allowed to stall whichever agent is broadcasting to it.
func TestSendClosesAClientThatCannotKeepUp(t *testing.T) {
	t.Parallel()

	// No WritePump: nothing drains the queue, which is exactly what a frozen
	// peer looks like from this side.
	client := NewClient(nil, nil)

	for range outboundBuffer {
		if err := client.Send(websocket.TextMessage, []byte("x")); err != nil {
			t.Fatalf("a send below the buffer size failed: %v", err)
		}
	}

	err := client.Send(websocket.TextMessage, []byte("one too many"))
	if !errors.Is(err, ErrClientBacklog) {
		t.Fatalf("expected ErrClientBacklog, got %v", err)
	}

	if !client.Closed() {
		t.Fatal("a client that overflowed its queue was left open")
	}

	// Every later send is refused rather than queued.
	if err := client.Send(websocket.TextMessage, []byte("after")); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("expected ErrClientClosed, got %v", err)
	}
}

// TestCloseWithReasonReachesThePeer covers the close frame an agent needs in
// order to tell a deploy or a rejection from a crash (STAB-6).
func TestCloseWithReasonReachesThePeer(t *testing.T) {
	t.Parallel()

	client, peer := dialClient(t)

	client.CloseWithReason(websocket.CloseServiceRestart, "server shutting down")

	if err := peer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}

	_, _, err := peer.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseServiceRestart) {
		t.Fatalf("expected close %d, got %v", websocket.CloseServiceRestart, err)
	}

	select {
	case <-client.Finished():
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never finished")
	}

	if !client.Closed() {
		t.Fatal("a closed client did not report itself closed")
	}
}

// TestCloseIsIdempotent covers the concurrency guarantee both Close and
// CloseWithReason document: any goroutine, any number of times, first call
// decides the code.
func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	client, _ := dialClient(t)

	var wg sync.WaitGroup

	for i := range 10 {
		wg.Go(func() {
			if i%2 == 0 {
				client.Close()
				return
			}

			client.CloseWithReason(websocket.CloseTryAgainLater, "later")
		})
	}

	wg.Wait()

	select {
	case <-client.Finished():
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never finished")
	}
}

// TestNilClientIsClosed covers the nil guards: a connection that is already
// gone is reported as closed rather than dereferenced.
func TestNilClientIsClosed(t *testing.T) {
	t.Parallel()

	var client *Client

	if !client.Closed() {
		t.Fatal("a nil client must report itself closed")
	}

	if err := client.Send(websocket.TextMessage, nil); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("expected ErrClientClosed, got %v", err)
	}

	client.Close()
	client.CloseWithReason(websocket.CloseNormalClosure, "")
}

// TestGracefulCloseFlushesQueuedMessages covers a client asking to close right
// after other commands: the replies already queued for it are written before
// the close frame. The writer used to pick at random between the queue and the
// close signal, so a reply queued a moment before a close was lost about half
// the time; the repetitions make that loss certain to show.
func TestGracefulCloseFlushesQueuedMessages(t *testing.T) {
	t.Parallel()

	for range 20 {
		client, peer, start := dialPausedClient(t)

		for _, body := range []string{"first", "second"} {
			if err := client.Send(websocket.TextMessage, []byte(body)); err != nil {
				t.Fatalf("failed to queue %q: %v", body, err)
			}
		}

		// Both the queue and the close are pending when the writer starts.
		client.CloseWithReason(websocket.CloseNormalClosure, "")
		start()

		for _, want := range []string{"first", "second"} {
			if err := peer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatalf("failed to set deadline: %v", err)
			}

			_, got, err := peer.ReadMessage()
			if err != nil {
				t.Fatalf("lost %q to the close: %v", want, err)
			}

			if string(got) != want {
				t.Fatalf("expected %q, got %q", want, got)
			}
		}

		_, _, err := peer.ReadMessage()

		var closeErr *websocket.CloseError
		if !errors.As(err, &closeErr) || closeErr.Code != websocket.CloseNormalClosure {
			t.Fatalf("expected a normal closure after the flush, got %v", err)
		}
	}
}
