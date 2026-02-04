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
		conn, err := getConn(t, connIdx)
		if err != nil {
			return err
		}

		data, err := h.service.ReadTestData(testID)
		if err != nil && stderrors.Is(err, runs.ErrTestNotFound) {
			data = t.GetData()
			err = nil
		}
		if err != nil {
			return errors.Wrap(err, "could not load data")
		}

		return conn.WriteMessage(websocket.BinaryMessage, data)
	case CommandUpdateData:
		if err := h.service.UpdateTestData(testID, m.Content.Bytes); err != nil {
			return errors.Wrap(err, "could not store data")
		}

		return nil
	case CommandGetConnectionCount:
		conn, err := getConn(t, connIdx)
		if err != nil {
			return err
		}

		return wsutil.SendMessage(
			conn,
			CommandGetConnectionCount,
			struct {
				Count int `json:"count"`
			}{Count: t.ConnectionCount()},
		)
	case CommandWaitCheckpoint:
		if _, err := getConn(t, connIdx); err != nil {
			return err
		}

		return waitCheckPoint(m.Content.Bytes, connIdx, t)
	case CommandClose:
		conn, err := getConn(t, connIdx)
		if err != nil {
			return err
		}

		return conn.Close()
	default:
		return errors.Errorf("received non existing command: %s", m.Command)
	}
}

func getConn(t *runs.Test, idx int) (*websocket.Conn, error) {
	conn := t.GetConnection(idx)
	if conn == nil {
		return nil, errors.New("connection not found")
	}

	return conn, nil
}
