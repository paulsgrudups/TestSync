package wsutil

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
)

// HandlerFunc describes the signature for WS handlers.
type HandlerFunc func(b []byte)

// Message describes the body that WS should receive.
type Message struct {
	Command string     `json:"command"`
	Content RawMessage `json:"content"`
}

// RawMessage describes raw message bytes with custom JSON marshalling and
// unmarshalling to avoid encoding byte array to base64. This way we can decode
// content only when needed in WS handler.
type RawMessage struct {
	Bytes []byte
}

// UnmarshalJSON guarantees proper parsing of raw message from JSON string.
func (rm *RawMessage) UnmarshalJSON(body []byte) error {
	rm.Bytes = body
	return nil
}

// MarshalJSON returns message bytes.
func (rm RawMessage) MarshalJSON() ([]byte, error) {
	return rm.Bytes, nil
}

// Connect creates a new WebSocket connection to specified endpoint. Uses oAuth
// client to authenticate connection.
func Connect(url string) (*websocket.Conn, *http.Response, error) {
	return websocket.DefaultDialer.Dial(url, http.Header{})
}

// SendMessage marshals and queues a message for the provided WebSocket
// client. This function uses Message struct to send messages in correct
// format. The message is written by the client's own writer, so this call
// never blocks and never races another writer.
func SendMessage(client *Client, cmd string, content any) error {
	if client == nil {
		return errors.New("no websocket connection provided")
	}

	c, err := json.Marshal(content)
	if err != nil {
		return errors.Wrap(err, "could not marshal command content")
	}

	message, err := json.Marshal(Message{
		Command: cmd,
		Content: RawMessage{Bytes: c},
	})
	if err != nil {
		return errors.Wrap(err, "could not marshal message for WebSocket")
	}

	err = client.Send(websocket.TextMessage, message)
	if err != nil {
		return errors.Wrap(err, "could not send WebSocket message")
	}

	return nil
}
