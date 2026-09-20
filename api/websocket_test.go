package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/paulsgrudups/testsync/internal/apptest"
)

// TestWebSocketSharesTheHTTPPort is the API-7 regression test. Agents used to
// need a second port, served by a second server with none of this router's
// middleware. The upgrade now goes through the same chain as every other
// request, which only works if each layer lets the connection be hijacked -
// [http.TimeoutHandler], for one, cannot, and must not wrap it.
func TestWebSocketSharesTheHTTPPort(t *testing.T) {
	t.Parallel()

	application := apptest.NewDefault(t)

	handler, err := NewRouter(application)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	header := http.Header{}
	header.Set("Authorization", "Basic dXNlcjpwYXNz") // user:pass

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/register/31"

	conn, resp, err := websocket.DefaultDialer.Dial(url, header)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}

		t.Fatalf("could not upgrade on the HTTP port (status %d): %v", status, err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	if err = conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"command":"get_connection_count","id":1}`)); err != nil {
		t.Fatalf("failed to send: %v", err)
	}

	if err = conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}

	_, reply, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("no reply over the shared port: %v", err)
	}

	if want := `{"command":"get_connection_count","id":1,"content":{"count":1}}`; string(reply) != want {
		t.Fatalf("\n got: %s\nwant: %s", reply, want)
	}

	// The upgrade is counted like any other request, as the 101 it was.
	if got := application.Metrics.Requests.Value("/register/{testID}", "101"); got != 1 {
		t.Fatalf("expected the upgrade counted as one 101, got %d", got)
	}
}

// TestWebSocketOnTheHTTPPortNeedsCredentials covers the shared port not
// becoming a way around authentication.
func TestWebSocketOnTheHTTPPortNeedsCredentials(t *testing.T) {
	t.Parallel()

	handler, err := NewRouter(apptest.NewDefault(t))
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	_, resp, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/register/32", nil,
	)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without credentials, got %v (%v)", resp, err)
	}
}
