package ws

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/paulsgrudups/testsync/wsutil"
)

func TestWebSocketCommands(t *testing.T) {
	t.Parallel()

	server := newInsecureServer(t)
	httpServer := httptest.NewServer(newWSRouter(server))
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/register/1"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("failed to dial ws: %v", err)
	}
	defer func() { _ = conn.Close() }()

	updatePayload := map[string]string{"data": "value"}
	if err = writeWS(conn, CommandUpdateData, updatePayload); err != nil {
		t.Fatalf("update_data failed: %v", err)
	}

	if err = conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}
	_, ack, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("update_data acknowledgement failed: %v", err)
	}

	if want := `{"command":"update_data","content":{"bytes":16}}`; string(ack) != want {
		t.Fatalf("unexpected update_data acknowledgement: %s", ack)
	}

	if err = writeWS(conn, CommandReadData, map[string]string{}); err != nil {
		t.Fatalf("read_data failed: %v", err)
	}

	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read_data response failed: %v", err)
	}

	// The payload comes back inside the envelope, as the JSON it was stored as.
	if want := `{"command":"read_data","content":{"data":"value"}}`; string(msg) != want {
		t.Fatalf("unexpected read_data reply: %s", msg)
	}

	if err = writeWS(conn, CommandGetConnectionCount, map[string]string{}); err != nil {
		t.Fatalf("get_connection_count failed: %v", err)
	}

	if err = conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}
	_, msg, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("get_connection_count response failed: %v", err)
	}

	var countMsg wsutil.Message
	if err = json.Unmarshal(msg, &countMsg); err != nil {
		t.Fatalf("failed to unmarshal count msg: %v", err)
	}
	if countMsg.Command != CommandGetConnectionCount {
		t.Fatalf("unexpected command: %s", countMsg.Command)
	}

	var countPayload struct {
		Count int `json:"count"`
	}
	if err = json.Unmarshal(countMsg.Content.Bytes, &countPayload); err != nil {
		t.Fatalf("failed to parse count payload: %v", err)
	}
	if countPayload.Count < 1 {
		t.Fatalf("expected count >= 1, got %d", countPayload.Count)
	}

	if err = writeWS(conn, CommandWaitCheckpoint, map[string]any{
		"identifier":   "checkpoint-1",
		"target_count": 1,
	}); err != nil {
		t.Fatalf("wait_checkpoint failed: %v", err)
	}

	if err = conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}
	_, msg, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("wait_checkpoint response failed: %v", err)
	}

	var cpMsg wsutil.Message
	if err := json.Unmarshal(msg, &cpMsg); err != nil {
		t.Fatalf("failed to unmarshal checkpoint msg: %v", err)
	}
	if cpMsg.Command != CommandWaitCheckpoint {
		t.Fatalf("unexpected command: %s", cpMsg.Command)
	}
}

func writeWS(conn *websocket.Conn, command string, content any) error {
	body, err := json.Marshal(content)
	if err != nil {
		return err
	}

	message, err := json.Marshal(wsutil.Message{Command: command, Content: wsutil.RawMessage{Bytes: body}})
	if err != nil {
		return err
	}

	return conn.WriteMessage(websocket.TextMessage, message)
}
