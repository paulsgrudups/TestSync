package ws

import (
	"encoding/json"

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

	// check if provided indentifier is already used, if it's already assigned
	// to test, then just add this connection. In case of a new identifier a
	// checkpoint is created.
	point := t.EnsureCheckpoint(check.Identifier, check.TargetCount)

	finished := point.IsFinished()
	if finished {
		// checkpoint has already finished, send a notification about
		// checkpoint's status.
		err = wsutil.SendMessage(
			t.GetConnection(connIdx),
			"wait_checkpoint",
			struct {
				Command    string `json:"command"`
				Identifier string `json:"identifier"`
				Finished   bool   `json:"finished"`
			}{
				Command:    "wait_checkpoint",
				Identifier: point.Identifier,
				Finished:   finished,
			},
		)
		if err != nil {
			return errors.Wrap(err, "could not send checkpoint update")
		}
	}

	point.AddConnection(connIdx)

	return nil
}
