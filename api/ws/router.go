package ws

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"

	log "github.com/sirupsen/logrus"
)

// maxMessageBytes bounds a single inbound WebSocket frame, mirroring the 10 MiB
// cap the HTTP path applies to request bodies. An oversized frame is rejected
// from its header, before its payload is read or allocated.
const maxMessageBytes = 10 << 20

var (
	// upgrader is shared by every request, so it must never be mutated after
	// initialisation: Upgrade reads its fields from every connection goroutine
	// at once, and assigning to them per request is a data race.
	//
	// CheckOrigin deliberately accepts every origin, which is the behaviour the
	// non-browser agents rely on today. Replacing it with a configured
	// allow-list is tracked separately (SEC-4); doing it here would silently
	// break them.
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(_ *http.Request) bool { return true },
	}
)

func newWSRouter(s *Server) http.Handler {
	router := mux.NewRouter().StrictSlash(true)

	// A panic must cost at most one connection, never the process.
	router.Use(utils.RecoverPanics)

	router.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := fmt.Fprintln(w, "WebSocket, reporting for duty!"); err != nil {
			log.Debugf("failed to write ws root response: %v", err)
		}
	})

	subrouter := router.PathPrefix("/register").Subrouter().StrictSlash(true)
	s.register(subrouter)

	return router
}

func (s *Server) register(r *mux.Router) {
	// Bounded to the digits an int64 can hold: a longer ID is not a test ID,
	// and letting it reach the handler only creates work to reject it.
	r.HandleFunc(`/{testID:[0-9]{1,19}}`, s.registerWS).
		Name("registerWebSocket").
		Methods(http.MethodGet)
}

func (s *Server) registerWS(w http.ResponseWriter, r *http.Request) {
	if !isUserAuthorized(w, r) {
		return
	}

	// The ID is parsed before the upgrade. Afterwards the ResponseWriter is
	// hijacked and can no longer carry an HTTP error, and a connection that is
	// upgraded and then abandoned is a leaked socket with no reader (CONC-11).
	testID, err := parseTestID(r)
	if err != nil {
		log.Debugf("Rejecting WebSocket registration: %s", err.Error())
		utils.HTTPError(w, "Unable to parse testID as int", http.StatusBadRequest)

		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Errorf("Failed to upgrade connection: %s", err.Error())
		return
	}

	log.Info("Connection established to WebSocket")

	// The connection now belongs to the reader, which closes it on every exit
	// path, panics included.
	go s.reader(conn, testID)
}

func (s *Server) reader(conn *websocket.Conn, testID int) {
	// Deferred first so that it runs last: the connection is already closed by
	// the time the panic is logged.
	defer utils.RecoverGoroutine("websocket reader")

	// The client owns every write to this connection, including keepalive
	// pings: gorilla/websocket panics on concurrent writers, and a checkpoint
	// release is written by whichever agent completed the barrier.
	client := wsutil.NewClient(conn)
	go client.WritePump()

	defer func() {
		client.Close()

		if err := conn.Close(); err != nil {
			log.Debugf("failed to close websocket connection: %v", err)
		}
	}()

	pongWait := s.pongWaitDuration()

	// Keepalive: the writer pings, and a peer that stops answering runs out of
	// read deadline instead of blocking this goroutine forever (CONC-10). The
	// read limit rejects an oversized frame from its header (STAB-2). The
	// ReadTimeout on the WS http.Server does not apply to an upgraded
	// connection and provides none of this.
	conn.SetReadLimit(maxMessageBytes)

	if err := conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		log.Errorf("Failed to set read deadline: %s", err.Error())
		return
	}

	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	r := runs.EnsureTest(testID, func() *runs.Test {
		return &runs.Test{
			Created:     time.Now(),
			Connections: []*wsutil.Client{},
		}
	})

	idx := r.AddConnection(client)

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

// parseTestID reads the test ID out of the request path. Unlike runs.GetPathID
// it writes nothing: the caller decides how to report the failure, which for a
// WebSocket route has to happen before the connection is hijacked.
func parseTestID(r *http.Request) (int, error) {
	raw := mux.Vars(r)["testID"]

	id, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("unable to parse testID %q as int: %w", raw, err)
	}

	return id, nil
}

// isUserAuthorized checks if provided request has set correct authorization
// headers. It authenticates through the same validator as the HTTP server
// (auth.Shared), so the two paths cannot disagree about who is allowed in
// (SEC-1), and an unconfigured validator denies rather than opens.
func isUserAuthorized(w http.ResponseWriter, r *http.Request) bool {
	validator := auth.Shared()
	if validator.Disabled() {
		return true
	}

	user, pass, ok := r.BasicAuth()
	if !ok {
		// Deprecated fallback for clients that cannot set headers (SEC-3).
		// Query strings leak into proxy and ingress logs; it is kept only for
		// backwards compatibility and will be removed.
		user = r.URL.Query().Get("username")
		pass = r.URL.Query().Get("password")

		if user == "" && pass == "" {
			log.Debug("Could not get basic auth")
			utils.HTTPError(w, "Request not authorized", http.StatusUnauthorized)

			return false
		}

		log.Warn(
			"Deprecated: WebSocket credentials were supplied as query " +
				"parameters, which leak into proxy and access logs. Use the " +
				"Authorization header instead.",
		)
	}

	if !validator.Validate(user, pass) {
		log.Debug("Could not validate user, invalid credentials")
		utils.HTTPError(w, "Request not authorized", http.StatusUnauthorized)

		return false
	}

	return true
}
