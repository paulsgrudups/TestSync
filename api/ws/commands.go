package ws

import (
	"encoding/json"
	"fmt"

	stderrors "errors"

	"github.com/pkg/errors"

	"github.com/paulsgrudups/testsync/api/runs"
)

// Command... describes available commands for websocket connection.
const (
	CommandReadData           = "read_data"
	CommandUpdateData         = "update_data"
	CommandGetConnectionCount = "get_connection_count"
	CommandWaitCheckpoint     = "wait_checkpoint"
	CommandClose              = "close"

	// CommandError is not a command a client sends: it is the reply a client
	// receives when its command was refused. It carries an [ErrorContent].
	CommandError = "error"
)

// Code... are the stable identifiers carried by an "error" reply. They name
// the limit that was hit, so a client can react to the condition rather than
// parse a sentence (STAB-3, SEC-8).
const (
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

	// Error is the human-readable reason, for logs and for a developer
	// reading a failed test run.
	Error string `json:"error"`
}

func waitCheckPoint(b []byte, connID runs.ConnID, t *runs.Test) error {
	var check struct {
		TargetCount int    `json:"target_count"`
		Identifier  string `json:"identifier"`
		TimeoutMS   int64  `json:"timeout_ms"`
	}

	err := json.Unmarshal(b, &check)
	if err != nil {
		return errors.Wrap(err, "could not unmarshal checkpoint data")
	}

	// Validate before touching any state: an omitted target_count decodes as
	// zero, which used to create a barrier that released on its first join and
	// left the agents unsynchronized without telling anybody.
	if check.Identifier == "" {
		return stderrors.New("checkpoint identifier must not be empty")
	}

	if check.TargetCount < 1 {
		return fmt.Errorf(
			"checkpoint %q target count must be at least 1, got %d",
			check.Identifier, check.TargetCount,
		)
	}

	// timeout_ms is optional: an omitted or zero field asks for the server
	// default, and an unreasonably large one is clamped rather than honoured.
	// A negative one is a client bug worth reporting.
	if check.TimeoutMS < 0 {
		return fmt.Errorf(
			"checkpoint %q timeout must not be negative, got %d ms",
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
	)
}
