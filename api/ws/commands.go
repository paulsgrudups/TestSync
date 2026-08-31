package ws

import (
	"encoding/json"
	"fmt"

	stderrors "errors"

	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/wsutil"
	"github.com/pkg/errors"
)

// Command... describes available commands for websocket connection.
const (
	CommandReadData           = "read_data"
	CommandUpdateData         = "update_data"
	CommandGetConnectionCount = "get_connection_count"
	CommandWaitCheckpoint     = "wait_checkpoint"
	CommandClose              = "close"
)

func waitCheckPoint(b []byte, connIdx int, t *runs.Test) error {
	var check struct {
		TargetCount int    `json:"target_count"`
		Identifier  string `json:"identifier"`
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

	// Joining is synchronous and never blocks. If this connection is the one
	// that completes the checkpoint, every member has been notified by the
	// time this returns.
	if !t.JoinCheckpoint(check.Identifier, check.TargetCount, connIdx) {
		return nil
	}

	// The checkpoint had already finished before this connection joined, so
	// send it the checkpoint's status instead.
	err = wsutil.SendMessage(
		t.GetConnection(connIdx),
		"wait_checkpoint",
		struct {
			Command    string `json:"command"`
			Identifier string `json:"identifier"`
			Finished   bool   `json:"finished"`
		}{
			Command:    "wait_checkpoint",
			Identifier: check.Identifier,
			Finished:   true,
		},
	)
	if err != nil {
		return errors.Wrap(err, "could not send checkpoint update")
	}

	return nil
}
