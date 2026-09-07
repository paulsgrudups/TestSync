package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/gorilla/websocket"

	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"
)

// CommandHandler processes WebSocket commands.
type CommandHandler struct {
	service *runs.Service
	log     *slog.Logger
}

// NewCommandHandler creates a handler over the given service. A nil logger
// discards.
func NewCommandHandler(service *runs.Service, logger *slog.Logger) *CommandHandler {
	if logger == nil {
		logger = utils.DiscardLogger()
	}

	return &CommandHandler{service: service, log: logger}
}

// Handle processes a single WebSocket message. A command that is refused
// because it would exceed a configured limit is answered with an "error"
// message naming the limit, so that the agent is told rather than left to
// guess why nothing happened (STAB-3, SEC-8).
func (h *CommandHandler) Handle(
	ctx context.Context, testID int, connID runs.ConnID, body []byte, t *runs.Test,
) error {
	var m wsutil.Message
	if err := json.Unmarshal(body, &m); err != nil {
		return fmt.Errorf("could not unmarshal message: %w", err)
	}

	h.log.DebugContext(ctx, "websocket command received",
		"test_id", testID, "conn_id", connID, "command", m.Command,
	)

	err := h.dispatch(ctx, testID, connID, m, t)
	if err != nil {
		h.reportRejection(ctx, t, connID, err)
	}

	return err
}

// dispatch runs one decoded command.
func (h *CommandHandler) dispatch(
	ctx context.Context, testID int, connID runs.ConnID, m wsutil.Message, t *runs.Test,
) error {
	switch m.Command {
	case CommandReadData:
		client, err := getClient(t, connID)
		if err != nil {
			return err
		}

		// A test with no stored data is not an error here: the agent gets an
		// empty message rather than a failure.
		data, err := h.service.ReadTestData(ctx, testID)
		if errors.Is(err, runs.ErrTestNotFound) {
			data, err = nil, nil
		}
		if err != nil {
			return fmt.Errorf("could not load data: %w", err)
		}

		return client.Send(websocket.BinaryMessage, data)
	case CommandUpdateData:
		if err := h.service.UpdateTestData(ctx, testID, m.Content.Bytes); err != nil {
			return fmt.Errorf("could not store data: %w", err)
		}

		return nil
	case CommandGetConnectionCount:
		client, err := getClient(t, connID)
		if err != nil {
			return err
		}

		return wsutil.SendMessage(
			client,
			CommandGetConnectionCount,
			struct {
				Count int `json:"count"`
			}{Count: t.ConnectionCount()},
		)
	case CommandWaitCheckpoint:
		if _, err := getClient(t, connID); err != nil {
			return err
		}

		return waitCheckPoint(m.Content.Bytes, connID, t)
	case CommandClose:
		client, err := getClient(t, connID)
		if err != nil {
			return err
		}

		client.Close()

		return nil
	default:
		return fmt.Errorf("received non existing command: %s", m.Command)
	}
}

// reportRejection tells the client which limit it hit. Anything else is an
// internal failure the client cannot act on, and is only logged.
func (h *CommandHandler) reportRejection(
	ctx context.Context, t *runs.Test, connID runs.ConnID, err error,
) {
	code, ok := rejectionCode(err)
	if !ok {
		return
	}

	client := t.GetConnection(connID)
	if client == nil {
		return
	}

	sendErr := wsutil.SendMessage(client, CommandError, ErrorContent{
		Code:  code,
		Error: err.Error(),
	})
	if sendErr != nil {
		h.log.ErrorContext(ctx, "could not report a rejection to the client",
			"code", code, "error", sendErr, "conn_id", connID,
		)
	}
}

// rejectionCode maps a refusal to the stable code the client receives.
func rejectionCode(err error) (string, bool) {
	switch {
	case errors.Is(err, runs.ErrDataTooLarge):
		return CodePayloadTooLarge, true
	case errors.Is(err, runs.ErrCheckpointLimitReached):
		return CodeCheckpointLimitReached, true
	case errors.Is(err, runs.ErrTestLimitReached):
		return CodeTestLimitReached, true
	case errors.Is(err, runs.ErrConnectionLimitReached):
		return CodeConnectionLimitReached, true
	default:
		return "", false
	}
}

func getClient(t *runs.Test, connID runs.ConnID) (*wsutil.Client, error) {
	client := t.GetConnection(connID)
	if client == nil {
		return nil, errors.New("connection not found")
	}

	return client, nil
}
