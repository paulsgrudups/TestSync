package wsutil

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestRawMessageAvoidsBase64 covers the reason RawMessage exists. A []byte
// field marshals to base64 in encoding/json, which would silently change every
// payload on the wire and break every existing client.
func TestRawMessageAvoidsBase64(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(Message{
		Command: "update_data",
		Content: RawMessage{Bytes: []byte(`{"data":"value"}`)},
	})
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	const want = `{"command":"update_data","content":{"data":"value"}}`

	if string(encoded) != want {
		t.Fatalf("wire format changed:\n got: %s\nwant: %s", encoded, want)
	}
}

// TestRawMessageRoundTrip covers decoding: the content is handed back exactly
// as it arrived, undecoded, for the command handler to interpret.
func TestRawMessageRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "object", body: `{"command":"c","content":{"a":1}}`, want: `{"a":1}`},
		{name: "string", body: `{"command":"c","content":"plain"}`, want: `"plain"`},
		{name: "array", body: `{"command":"c","content":[1,2]}`, want: `[1,2]`},
		{name: "null", body: `{"command":"c","content":null}`, want: `null`},
		{name: "number", body: `{"command":"c","content":7}`, want: `7`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var m Message
			if err := json.Unmarshal([]byte(tc.body), &m); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			if m.Command != "c" {
				t.Fatalf("expected command %q, got %q", "c", m.Command)
			}

			if string(m.Content.Bytes) != tc.want {
				t.Fatalf("expected content %s, got %s", tc.want, m.Content.Bytes)
			}
		})
	}
}

// TestSendMessageRejectsNilClient covers the guard: a command aimed at a
// connection that is already gone is an error, not a panic.
func TestSendMessageRejectsNilClient(t *testing.T) {
	t.Parallel()

	if err := SendMessage(nil, "cmd", struct{}{}); err == nil {
		t.Fatal("expected an error for a nil client")
	}
}

// TestSendMessageReportsUnmarshalableContent covers the other failure: content
// that cannot be encoded is reported rather than sent as something else.
func TestSendMessageReportsUnmarshalableContent(t *testing.T) {
	t.Parallel()

	client := NewClient(nil, nil)

	err := SendMessage(client, "cmd", make(chan int))
	if err == nil {
		t.Fatal("expected an error for unmarshalable content")
	}

	var unsupported *json.UnsupportedTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected a json.UnsupportedTypeError, got %v", err)
	}
}

// TestSendMessageQueuesTheEncodedFrame checks that what SendMessage hands the
// client is the exact wire form asserted above.
func TestSendMessageQueuesTheEncodedFrame(t *testing.T) {
	t.Parallel()

	client := NewClient(nil, nil)

	if err := SendMessage(client, "wait_checkpoint", map[string]int{"target_count": 2}); err != nil {
		t.Fatalf("failed to queue: %v", err)
	}

	select {
	case msg := <-client.out:
		const want = `{"command":"wait_checkpoint","content":{"target_count":2}}`
		if string(msg.data) != want {
			t.Fatalf("queued frame:\n got: %s\nwant: %s", msg.data, want)
		}
	default:
		t.Fatal("SendMessage queued nothing")
	}
}
