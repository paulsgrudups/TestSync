package wsutil

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorilla/websocket"
)

// Message is the envelope of every WebSocket frame, in both directions. It is
// protocol v1; see PROTOCOL.md.
type Message struct {
	Command string `json:"command"`

	// ID is an optional correlation token chosen by the client: a JSON string
	// or number. The server echoes it verbatim on the reply to that command
	// and omits it when the command carried none.
	ID json.RawMessage `json:"id,omitempty"`

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

// MarshalJSON returns message bytes. Empty content is written as null, which
// is what a command without content decodes from.
func (rm RawMessage) MarshalJSON() ([]byte, error) {
	if len(rm.Bytes) == 0 {
		return []byte("null"), nil
	}

	return rm.Bytes, nil
}

// SendMessage marshals and queues a message for the provided WebSocket
// client. This function uses Message struct to send messages in correct
// format. The message is written by the client's own writer, so this call
// never blocks and never races another writer.
func SendMessage(client *Client, cmd string, content any) error {
	return SendReply(client, cmd, nil, content)
}

// SendReply is [SendMessage] for the reply to a particular command: id is the
// correlation token that command carried, echoed back unchanged. A nil id is
// omitted. Content that is already encoded, a [json.RawMessage], is sent as
// is.
func SendReply(client *Client, cmd string, id json.RawMessage, content any) error {
	if client == nil {
		return errors.New("no websocket connection provided")
	}

	c, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("could not marshal command content: %w", err)
	}

	message, err := json.Marshal(Message{
		Command: cmd,
		ID:      id,
		Content: RawMessage{Bytes: c},
	})
	if err != nil {
		return fmt.Errorf("could not marshal message for WebSocket: %w", err)
	}

	err = client.Send(websocket.TextMessage, message)
	if err != nil {
		return fmt.Errorf("could not send WebSocket message: %w", err)
	}

	return nil
}
