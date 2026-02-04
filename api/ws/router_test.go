package ws

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/storage"
	"github.com/paulsgrudups/testsync/wsutil"
)

func TestWebSocketCommands(t *testing.T) {
	runs.AllTests = make(map[int]*runs.Test)
	runs.SetDataStore(storage.NewMemoryStore())
	runs.DefaultService = runs.NewService(nil)

	server := &Server{Handler: NewCommandHandler(nil)}
	httpServer := httptest.NewServer(newWSRouter(server))
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/register/1"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial ws: %v", err)
	}
	defer conn.Close()

	updatePayload := map[string]string{"data": "value"}
	if err := writeWS(conn, CommandUpdateData, updatePayload); err != nil {
		t.Fatalf("update_data failed: %v", err)
	}

	if err := writeWS(conn, CommandReadData, map[string]string{}); err != nil {
		t.Fatalf("read_data failed: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read_data response failed: %v", err)
	}

	expected, err := json.Marshal(updatePayload)
	if err != nil {
		t.Fatalf("failed to marshal expected payload: %v", err)
	}
	if string(msg) != string(expected) {
		t.Fatalf("unexpected read_data payload: %q", string(msg))
	}

	if err := writeWS(conn, CommandGetConnectionCount, map[string]string{}); err != nil {
		t.Fatalf("get_connection_count failed: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("get_connection_count response failed: %v", err)
	}

	var countMsg wsutil.Message
	if err := json.Unmarshal(msg, &countMsg); err != nil {
		t.Fatalf("failed to unmarshal count msg: %v", err)
	}
	if countMsg.Command != CommandGetConnectionCount {
		t.Fatalf("unexpected command: %s", countMsg.Command)
	}

	var countPayload struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(countMsg.Content.Bytes, &countPayload); err != nil {
		t.Fatalf("failed to parse count payload: %v", err)
	}
	if countPayload.Count < 1 {
		t.Fatalf("expected count >= 1, got %d", countPayload.Count)
	}

	if err := writeWS(conn, CommandWaitCheckpoint, map[string]interface{}{
		"identifier":   "checkpoint-1",
		"target_count": 1,
	}); err != nil {
		t.Fatalf("wait_checkpoint failed: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
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

func writeWS(conn *websocket.Conn, command string, content interface{}) error {
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
