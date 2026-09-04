package ws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/paulsgrudups/testsync/internal/app"
	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"
)

// Server describes WebSocket server with available handler functions.
type Server struct {
	HTTPServer *http.Server
	Handler    *CommandHandler

	// app carries the registry and the validator this server works against.
	// Both used to be package globals installed by startup, so a WebSocket
	// server could not be built for anything but the one process-wide
	// instance (CODE-1).
	app *app.App

	// listenErr carries a fatal listen error, such as a port already in use,
	// out of the accept goroutine. It used to be discarded there, leaving the
	// process running with no WebSocket server at all and nothing said about
	// it (STAB-7).
	listenErr chan error

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

// StartWebSocketServer launches a websocket server for the given application,
// on the port its configuration names. A fatal listen error is delivered on
// [Server.ListenErr] rather than crashing the accept goroutine or being
// swallowed.
func StartWebSocketServer(a *app.App) *Server {
	port := a.Config.WSPort

	s := &Server{
		Handler:   NewCommandHandler(a.Service),
		app:       a,
		listenErr: make(chan error, 1),
	}

	s.HTTPServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      newWSRouter(s),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  10 * time.Second,
	}

	go func() {
		defer utils.RecoverGoroutine("websocket listener")

		err := s.HTTPServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.listenErr <- fmt.Errorf("websocket server on port %d: %w", port, err)
		}
	}()

	return s
}

// ListenErr reports a fatal error from the accept loop. It yields at most one
// error, and never fires for an ordinary shutdown.
func (s *Server) ListenErr() <-chan error {
	if s == nil || s.listenErr == nil {
		return nil
	}

	return s.listenErr
}

// Shutdown gracefully stops the WebSocket server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.HTTPServer == nil {
		return nil
	}

	return s.HTTPServer.Shutdown(ctx)
}
