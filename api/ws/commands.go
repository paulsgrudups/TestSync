package ws

import (
	"encoding/json"
	"fmt"

	"github.com/paulsgrudups/testsync/api/runs"
)

// Subprotocol is the WebSocket subprotocol name of protocol v1. A client may
// offer it in Sec-WebSocket-Protocol and the server selects it; a client that
// offers no subprotocol at all is spoken to in v1 too. A future v2 will be
// served only to clients that ask for it by name.
const Subprotocol = "testsync.v1"

// maxRequestIDBytes bounds the correlation id a client may attach to a command.
// It is echoed back verbatim, so it is kept to something an id needs.
const maxRequestIDBytes = 128

// Command... describes available commands for websocket connection.
const (
	CommandReadData           = "read_data"
	CommandUpdateData         = "update_data"
	CommandGetConnectionCount = "get_connection_count"
	CommandWaitCheckpoint     = "wait_checkpoint"
	CommandClose              = "close"

	// CommandError is not a command a client sends: it is the reply a client
	// receives when its command failed. It carries an [ErrorContent].
	CommandError = "error"
)

// Code... are the stable identifiers carried by an "error" reply, so a client
// can react to the condition rather than parse a sentence (API-2, STAB-3,
// SEC-8). The set only grows within a protocol version.
const (
	// CodeInvalidMessage means the frame was not a valid envelope: not JSON,
	// or an id that is not a string or number of at most 128 bytes.
	CodeInvalidMessage = "invalid_message"

	// CodeUnknownCommand means the command name is not one the server knows.
	CodeUnknownCommand = "unknown_command"

	// CodeInvalidArgument means the command's content was missing or
	// malformed, such as a checkpoint without an identifier.
	CodeInvalidArgument = "invalid_argument"

	// CodeDataNotJSON means read_data found a payload that is not JSON, which
	// the WebSocket protocol cannot carry. It was stored over HTTP; read it
	// back over HTTP.
	CodeDataNotJSON = "data_not_json"

	// CodeInternalError means the server failed. The command may be retried.
	CodeInternalError = "internal_error"

	// CodePayloadTooLarge means the payload exceeded limits.max_data_bytes.
	CodePayloadTooLarge = "payload_too_large"

	// CodeCheckpointLimitReached means the run already holds
	// limits.max_checkpoints_per_test checkpoint identifiers.
	CodeCheckpointLimitReached = "checkpoint_limit_reached"

	// CodeTestLimitReached means the server already holds limits.max_tests
	// runs.
	CodeTestLimitReached = "test_limit_reached"

	// CodeConnectionLimitReached means the run already holds
	// limits.max_connections_per_test agents.
	CodeConnectionLimitReached = "connection_limit_reached"
)

// ErrorContent is the content of an "error" reply.
type ErrorContent struct {
	// Code is one of the Code... constants above.
	Code string `json:"code"`

	// Command names the command that failed, so a client that sends no ids
	// can still tell which one it was. It is empty when the frame could not
	// be read far enough to know.
	Command string `json:"command"`

	// Error is the human-readable reason, for logs and for a developer
	// reading a failed test run.
	Error string `json:"error"`
}

// commandError is a failure with the code the client is told. The message is
// the client's to read; the wrapped error, if any, is only logged.
type commandError struct {
	code    string
	message string
	err     error
}

func (e *commandError) Error() string {
	if e.err != nil {
		return e.message + ": " + e.err.Error()
	}

	return e.message
}

func (e *commandError) Unwrap() error { return e.err }

// invalidArgument reports content the command cannot use.
func invalidArgument(format string, args ...any) error {
	return &commandError{code: CodeInvalidArgument, message: fmt.Sprintf(format, args...)}
}

func waitCheckPoint(
	b []byte, connID runs.ConnID, requestID json.RawMessage, t *runs.Test,
) error {
	var check struct {
		TargetCount int    `json:"target_count"`
		Identifier  string `json:"identifier"`
		TimeoutMS   int64  `json:"timeout_ms"`
	}

	if err := json.Unmarshal(b, &check); err != nil {
		return invalidArgument("checkpoint content is not a JSON object: %v", err)
	}

	// Validate before touching any state: an omitted target_count decodes as
	// zero, which used to create a barrier that released on its first join and
	// left the agents unsynchronized without telling anybody.
	if check.Identifier == "" {
		return invalidArgument("checkpoint identifier must not be empty")
	}

	if check.TargetCount < 1 {
		return invalidArgument(
			"checkpoint %q target_count must be at least 1, got %d",
			check.Identifier, check.TargetCount,
		)
	}

	// timeout_ms is optional: an omitted or zero field asks for the server
	// default, and an unreasonably large one is clamped rather than honoured.
	// A negative one is a client bug worth reporting.
	if check.TimeoutMS < 0 {
		return invalidArgument(
			"checkpoint %q timeout_ms must not be negative, got %d",
			check.Identifier, check.TimeoutMS,
		)
	}

	// Joining is synchronous and never blocks. If this connection is the one
	// that completes the checkpoint, every member has been notified by the
	// time this returns. Otherwise the round ends on its own deadline or when
	// it loses a participant, and this connection is told either way.
	return t.JoinCheckpoint(
		check.Identifier,
		check.TargetCount,
		runs.CheckpointTimeout(check.TimeoutMS),
		connID,
		requestID,
	)
}
