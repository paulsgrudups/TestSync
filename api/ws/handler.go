package ws

import (
	"encoding/json"

	stderrors "errors"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"

	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/wsutil"

	log "github.com/sirupsen/logrus"
)

// CommandHandler processes WebSocket commands.
type CommandHandler struct {
	service *runs.Service
}

// NewCommandHandler creates a handler. If service is nil, uses runs.DefaultService.
func NewCommandHandler(service *runs.Service) *CommandHandler {
	if service == nil {
		service = runs.DefaultService
	}

	return &CommandHandler{service: service}
}

// Handle processes a single WebSocket message. A command that is refused
// because it would exceed a configured limit is answered with an "error"
// message naming the limit, so that the agent is told rather than left to
// guess why nothing happened (STAB-3, SEC-8).
func (h *CommandHandler) Handle(
	testID int, connID runs.ConnID, body []byte, t *runs.Test,
) error {
	var m wsutil.Message
	if err := json.Unmarshal(body, &m); err != nil {
		return errors.Wrap(err, "could not unmarshal message")
	}

	log.WithFields(log.Fields{
		"test_id": testID,
		"conn_id": connID,
		"command": m.Command,
	}).Debug("WS command received")

	err := h.dispatch(testID, connID, m, t)
	if err != nil {
		reportRejection(t, connID, err)
	}

	return err
}

// dispatch runs one decoded command.
func (h *CommandHandler) dispatch(
	testID int, connID runs.ConnID, m wsutil.Message, t *runs.Test,
) error {
	switch m.Command {
	case CommandReadData:
		client, err := getClient(t, connID)
		if err != nil {
			return err
		}

		// A test with no stored data is not an error here: the agent gets an
		// empty message rather than a failure.
		data, err := h.service.ReadTestData(testID)
		if stderrors.Is(err, runs.ErrTestNotFound) {
			data, err = nil, nil
		}
		if err != nil {
			return errors.Wrap(err, "could not load data")
		}

		return client.Send(websocket.BinaryMessage, data)
	case CommandUpdateData:
		if err := h.service.UpdateTestData(testID, m.Content.Bytes); err != nil {
			return errors.Wrap(err, "could not store data")
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
		return errors.Errorf("received non existing command: %s", m.Command)
	}
}

// reportRejection tells the client which limit it hit. Anything else is an
// internal failure the client cannot act on, and is only logged.
func reportRejection(t *runs.Test, connID runs.ConnID, err error) {
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
		log.Errorf("Could not report %s to the client: %s", code, sendErr.Error())
	}
}

// rejectionCode maps a refusal to the stable code the client receives.
func rejectionCode(err error) (string, bool) {
	switch {
	case stderrors.Is(err, runs.ErrDataTooLarge):
		return CodePayloadTooLarge, true
	case stderrors.Is(err, runs.ErrCheckpointLimitReached):
		return CodeCheckpointLimitReached, true
	case stderrors.Is(err, runs.ErrTestLimitReached):
		return CodeTestLimitReached, true
	case stderrors.Is(err, runs.ErrConnectionLimitReached):
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
