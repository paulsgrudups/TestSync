package ws

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/paulsgrudups/testsync/wsutil"
)

// Server describes WebSocket server with available handler functions.
type Server struct {
	HTTPServer *http.Server
	Handler    *CommandHandler

	// pongWait overrides how long a connection may stay silent before its
	// reader gives up on the peer. Zero, the only value used in production,
	// means wsutil.PongWait; tests shorten it so that reaping an unresponsive
	// peer does not take half a minute.
	pongWait time.Duration
}

// pongWaitDuration returns the read deadline extension for a connection.
func (s *Server) pongWaitDuration() time.Duration {
	if s != nil && s.pongWait > 0 {
		return s.pongWait
	}

	return wsutil.PongWait
}

// StartWebSocketServer launches a new websocket server and returns the port
// used by it.
func StartWebSocketServer(port int) *Server {
	s := &Server{Handler: NewCommandHandler(nil)}

	s.HTTPServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      newWSRouter(s),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  10 * time.Second,
	}
	go s.HTTPServer.ListenAndServe() //nolint:errcheck // listen error ignored because server shutdown is handled elsewhere

	return s
}

// Shutdown gracefully stops the WebSocket server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.HTTPServer == nil {
		return nil
	}

	return s.HTTPServer.Shutdown(ctx)
}
