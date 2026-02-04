package ws

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/utils"

	log "github.com/sirupsen/logrus"
)

var (
	// SyncClient defines sync client credentials.
	SyncClient utils.BasicCredentials

	upgrader = websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 1024}
)

func newWSRouter(s *Server) http.Handler {
	router := mux.NewRouter().StrictSlash(true)

	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "WebSocket, reporting for duty!")
	})

	subrouter := router.PathPrefix("/register").Subrouter().StrictSlash(true)
	s.register(subrouter)

	return router
}

func (s *Server) register(r *mux.Router) {
	r.HandleFunc(`/{testID:\d+}`, s.registerWS).
		Name("registerWebSocket").
		Methods(http.MethodGet)
}

func (s *Server) registerWS(w http.ResponseWriter, r *http.Request) {
	upgrader.CheckOrigin = func(r *http.Request) bool { return true }

	if !isUserAuthorized(w, r) {
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Errorf("Failed to upgrade connection: %s", err.Error())
		return
	}

	log.Info("Connection established to WebSocket")

	testID, err := runs.GetPathID(w, r, "testID")
	if err != nil {
		log.Errorf("Could not get path ID: %s", err.Error())
		return
	}

	go s.reader(conn, testID)
}

func (s *Server) reader(conn *websocket.Conn, testID int) {
	closeC := make(chan bool)
	defer close(closeC)

	go func() {
		for {
			select {
			case <-closeC:
				return
			case <-time.After(10 * time.Second):
				err := conn.WriteMessage(websocket.PingMessage, []byte("ping"))
				if err != nil {
					log.Errorf(
						"Could not send WS ping message: %s", err.Error(),
					)
				}
			}
		}
	}()

	r := runs.EnsureTest(testID, func() *runs.Test {
		return &runs.Test{
			Created:     time.Now(),
			Connections: []*websocket.Conn{},
			CheckPoints: make(map[string]*runs.Checkpoint),
		}
	})

	idx := r.AddConnection(conn)

	for {
		messageType, p, err := conn.ReadMessage()
		if err != nil {
			if messageType != -1 {
				log.Errorf(
					"Failed to read message for %d test: %s",
					testID, err.Error(),
				)
			} else {
				log.Infof(
					"WS connection closed for %d test: %s",
					testID, err.Error(),
				)
			}

			closeC <- true

			return
		}

		log.Infof("Received message: %s", string(p))

		handler := s.Handler
		if handler == nil {
			handler = NewCommandHandler(nil)
		}

		err = handler.Handle(testID, idx, p, r)
		if err != nil {
			log.Errorf("Failed to process message: %s", err.Error())
		}
	}
}

// isUserAuthorized checks if provided request has set correct authorization
// headers.
func isUserAuthorized(w http.ResponseWriter, r *http.Request) bool {
	if SyncClient.Username == "" && SyncClient.Password == "" {
		return true
	}

	user, pass, ok := r.BasicAuth()
	if !ok {
		user = r.URL.Query().Get("username")
		pass = r.URL.Query().Get("password")
		if user == "" && pass == "" {
			log.Debug("Could not get basic auth")
			utils.HTTPError(w, "Request not authorized", http.StatusUnauthorized)
			return false
		}
	}

	if user != SyncClient.Username || pass != SyncClient.Password {
		log.Debug("Could not validate user, invalid credentials")
		utils.HTTPError(w, "Request not authorized", http.StatusUnauthorized)
		return false
	}

	return true
}
