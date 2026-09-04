package ws

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"

	log "github.com/sirupsen/logrus"
)

// envelopeAllowance is the room a frame gets on top of limits.max_data_bytes
// for its command envelope. Without it a payload one byte over the limit would
// be killed by the frame cap with close code 1009 instead of being answered
// with the "error" reply that names the limit; with it, only a frame that is
// wildly oversized is rejected from its header.
const envelopeAllowance = 1 << 10

// rejectFlushWait bounds how long a rejected registration waits for its close
// frame to reach the wire. The reader closes the socket on its way out, so
// without this the frame the agent needs in order to tell a rejection from a
// crash could be lost to the race.
const rejectFlushWait = 2 * time.Second

// maxMessageBytes bounds a single inbound WebSocket frame. It follows the same
// limits.max_data_bytes that bounds an HTTP request body and a stored payload,
// so the three caps cannot drift apart. An oversized frame is rejected from
// its header, before its payload is read or allocated.
func (s *Server) maxMessageBytes() int64 {
	return s.app.Registry.Limits().MaxDataBytes + envelopeAllowance
}

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
	if !isUserAuthorized(w, r, s.app.Auth) {
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

	// A server that is already holding its maximum number of runs says so with
	// an HTTP status, before the socket is upgraded and an error can only be
	// delivered as a close frame (STAB-3).
	if admitErr := s.app.Registry.CanAdmit(testID); admitErr != nil {
		log.Warnf("Rejecting WebSocket registration: %s", admitErr.Error())
		utils.HTTPError(
			w,
			"Too many active test runs; retry once running suites finish",
			http.StatusServiceUnavailable,
		)

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
	conn.SetReadLimit(s.maxMessageBytes())

	if err := conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		log.Errorf("Failed to set read deadline: %s", err.Error())
		return
	}

	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	r, err := s.app.Registry.Ensure(testID)
	if err != nil {
		// The socket is already upgraded, so the rejection is a close frame
		// rather than an HTTP status. 1013 (try again later) says the agent
		// may retry, which is true of both limits below.
		log.Warnf("Rejecting connection for test %d: %s", testID, err.Error())
		rejectConnection(client, "too many active test runs")

		return
	}

	// The connection is registered under an ID owned by this reader alone.
	// However the goroutine exits, panics included, the connection stops being
	// counted and gives up its slot in every barrier it joined, instead of
	// holding the other agents there (CONC-5, CONC-6).
	connID, err := r.AddConnection(client)
	if err != nil {
		log.Warnf("Rejecting connection for test %d: %s", testID, err.Error())
		rejectConnection(client, "connection limit reached for this test run")

		return
	}

	defer r.RemoveConnection(connID)

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

		err = s.Handler.Handle(testID, connID, p, r)
		if err != nil {
			log.Errorf("Failed to process message: %s", err.Error())
		}
	}
}

// rejectConnection turns an agent away with close code 1013 (try again later)
// and waits, briefly, for the frame to be written.
func rejectConnection(client *wsutil.Client, reason string) {
	client.CloseWithReason(websocket.CloseTryAgainLater, reason)

	timer := time.NewTimer(rejectFlushWait)
	defer timer.Stop()

	select {
	case <-client.Finished():
	case <-timer.C:
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
// headers. The validator is the same one the HTTP server holds, carried on the
// App, so the two paths cannot disagree about who is allowed in (SEC-1), and a
// validator that was never configured denies rather than opens.
func isUserAuthorized(w http.ResponseWriter, r *http.Request, validator *auth.Validator) bool {
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
