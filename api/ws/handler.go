package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/gorilla/websocket"

	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/internal/metrics"
	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"
)

// CommandHandler processes WebSocket commands.
type CommandHandler struct {
	service  *runs.Service
	commands *metrics.CounterVec
	log      *slog.Logger
}

// NewCommandHandler creates a handler over the given service. commands counts
// the handled commands by name and outcome, and may be nil. A nil logger
// discards.
func NewCommandHandler(
	service *runs.Service, commands *metrics.CounterVec, logger *slog.Logger,
) *CommandHandler {
	if logger == nil {
		logger = utils.DiscardLogger()
	}

	return &CommandHandler{service: service, commands: commands, log: logger}
}

// Handle processes a single WebSocket message and answers it. Every command
// gets exactly one reply: its result, or an "error" naming what went wrong
// (API-2). The only exceptions are close, which is answered by the close
// itself, and wait_checkpoint, whose result is the release that ends its
// round. The returned error is for the caller's log.
func (h *CommandHandler) Handle(
	ctx context.Context, testID int, connID runs.ConnID, body []byte, t *runs.Test,
) error {
	var m wsutil.Message
	if err := json.Unmarshal(body, &m); err != nil {
		err = &commandError{
			code: CodeInvalidMessage, message: "the message is not a JSON envelope", err: err,
		}
		h.reportFailure(ctx, t, connID, wsutil.Message{}, err)
		h.count("", err)

		return err
	}

	if err := validateRequestID(m.ID); err != nil {
		// The id is not echoed: it is the thing that could not be trusted.
		h.reportFailure(ctx, t, connID, wsutil.Message{Command: m.Command}, err)
		h.count(m.Command, err)

		return err
	}

	h.log.DebugContext(ctx, "websocket command received",
		"test_id", testID, "conn_id", connID, "command", m.Command,
	)

	err := h.dispatch(ctx, testID, connID, m, t)
	if err != nil {
		h.reportFailure(ctx, t, connID, m, err)
	}

	h.count(m.Command, err)

	return err
}

// count records one handled command. A command name the server does not know
// is counted as "unknown" - the name is client-supplied, and a label per
// misspelling would be a series per misspelling.
func (h *CommandHandler) count(command string, err error) {
	switch command {
	case CommandReadData, CommandUpdateData, CommandGetConnectionCount,
		CommandWaitCheckpoint, CommandClose:
	default:
		command = "unknown"
	}

	outcome := "ok"
	if err != nil {
		outcome, _ = failureCode(err)
	}

	h.commands.Inc(command, outcome)
}

// validateRequestID accepts an absent id, a JSON string or a JSON number, of
// at most maxRequestIDBytes.
func validateRequestID(id json.RawMessage) error {
	if len(id) == 0 {
		return nil
	}

	invalid := &commandError{
		code: CodeInvalidMessage,
		message: fmt.Sprintf(
			"id must be a JSON string or number of at most %d bytes", maxRequestIDBytes,
		),
	}

	if len(id) > maxRequestIDBytes {
		return invalid
	}

	var v any
	if err := json.Unmarshal(id, &v); err != nil {
		return invalid
	}

	switch v.(type) {
	case string, float64:
		return nil
	default:
		return invalid
	}
}

// dispatch runs one decoded command and sends its result.
func (h *CommandHandler) dispatch(
	ctx context.Context, testID int, connID runs.ConnID, m wsutil.Message, t *runs.Test,
) error {
	client, err := getClient(t, connID)
	if err != nil {
		return err
	}

	switch m.Command {
	case CommandReadData:
		data, err := h.readData(ctx, testID)
		if err != nil {
			return err
		}

		return wsutil.SendReply(client, CommandReadData, m.ID, data)
	case CommandUpdateData:
		// Absent content decodes to nothing and "content": null to the four
		// bytes null; neither is a payload anybody meant to store.
		if len(m.Content.Bytes) == 0 || string(m.Content.Bytes) == "null" {
			return invalidArgument("update_data needs content: the payload to store")
		}

		if err := h.service.UpdateTestData(ctx, testID, m.Content.Bytes); err != nil {
			return fmt.Errorf("could not store data: %w", err)
		}

		return wsutil.SendReply(client, CommandUpdateData, m.ID, struct {
			Bytes int `json:"bytes"`
		}{Bytes: len(m.Content.Bytes)})
	case CommandGetConnectionCount:
		return wsutil.SendReply(client, CommandGetConnectionCount, m.ID, struct {
			Count int `json:"count"`
		}{Count: t.ConnectionCount()})
	case CommandWaitCheckpoint:
		return waitCheckPoint(m.Content.Bytes, connID, m.ID, t)
	case CommandClose:
		// A normal closure, after the replies to anything sent before it.
		client.CloseWithReason(websocket.CloseNormalClosure, "")

		return nil
	default:
		return &commandError{
			code:    CodeUnknownCommand,
			message: fmt.Sprintf("unknown command %q", m.Command),
		}
	}
}

// readData loads the run's payload as the content of a read_data reply: the
// stored JSON value itself, or null when nothing is stored. A payload that is
// not JSON was written over HTTP and cannot be carried here.
func (h *CommandHandler) readData(ctx context.Context, testID int) (json.RawMessage, error) {
	data, err := h.service.ReadTestData(ctx, testID)
	if errors.Is(err, runs.ErrTestNotFound) || (err == nil && len(data) == 0) {
		return json.RawMessage("null"), nil
	}

	if err != nil {
		return nil, fmt.Errorf("could not load data: %w", err)
	}

	if !json.Valid(data) {
		return nil, &commandError{
			code: CodeDataNotJSON,
			message: "the stored payload is not JSON; " +
				"read it with GET /tests/{testID} instead",
		}
	}

	return data, nil
}

// reportFailure answers a failed command with an "error" reply.
func (h *CommandHandler) reportFailure(
	ctx context.Context, t *runs.Test, connID runs.ConnID, m wsutil.Message, err error,
) {
	client := t.GetConnection(connID)
	if client == nil {
		return
	}

	code, message := failureCode(err)

	sendErr := wsutil.SendReply(client, CommandError, m.ID, ErrorContent{
		Code:    code,
		Command: m.Command,
		Error:   message,
	})
	if sendErr != nil {
		h.log.ErrorContext(ctx, "could not report a failure to the client",
			"code", code, "error", sendErr, "conn_id", connID,
		)
	}
}

// failureCode maps a failure to the code and message the client receives. An
// unexpected failure is reported without its detail, which may describe the
// server's storage rather than anything the client did.
func failureCode(err error) (string, string) {
	var cmdErr *commandError

	switch {
	case errors.As(err, &cmdErr):
		return cmdErr.code, cmdErr.message
	case errors.Is(err, runs.ErrDataTooLarge):
		return CodePayloadTooLarge, err.Error()
	case errors.Is(err, runs.ErrCheckpointLimitReached):
		return CodeCheckpointLimitReached, err.Error()
	case errors.Is(err, runs.ErrTestLimitReached):
		return CodeTestLimitReached, err.Error()
	case errors.Is(err, runs.ErrConnectionLimitReached):
		return CodeConnectionLimitReached, err.Error()
	default:
		return CodeInternalError, "the server could not complete the command"
	}
}

// failureLogLevel is the level a failed command is logged at: error for a
// failure of the server's own, info for anything the client caused.
func failureLogLevel(err error) slog.Level {
	if code, _ := failureCode(err); code == CodeInternalError {
		return slog.LevelError
	}

	return slog.LevelInfo
}

func getClient(t *runs.Test, connID runs.ConnID) (*wsutil.Client, error) {
	client := t.GetConnection(connID)
	if client == nil {
		return nil, errors.New("connection not found")
	}

	return client, nil
}
