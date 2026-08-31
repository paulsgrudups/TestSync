package ws

import (
	"encoding/json"

	stderrors "errors"

	"github.com/gorilla/websocket"
	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/wsutil"
	"github.com/pkg/errors"

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

// Handle processes a single WebSocket message.
func (h *CommandHandler) Handle(testID int, connIdx int, body []byte, t *runs.Test) error {
	var m wsutil.Message
	if err := json.Unmarshal(body, &m); err != nil {
		return errors.Wrap(err, "could not unmarshal message")
	}

	log.WithFields(log.Fields{
		"test_id":  testID,
		"conn_idx": connIdx,
		"command":  m.Command,
	}).Debug("WS command received")

	switch m.Command {
	case CommandReadData:
		client, err := getClient(t, connIdx)
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
		client, err := getClient(t, connIdx)
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
		if _, err := getClient(t, connIdx); err != nil {
			return err
		}

		return waitCheckPoint(m.Content.Bytes, connIdx, t)
	case CommandClose:
		client, err := getClient(t, connIdx)
		if err != nil {
			return err
		}

		client.Close()

		return nil
	default:
		return errors.Errorf("received non existing command: %s", m.Command)
	}
}

func getClient(t *runs.Test, idx int) (*wsutil.Client, error) {
	client := t.GetConnection(idx)
	if client == nil {
		return nil, errors.New("connection not found")
	}

	return client, nil
}
