package ws

import (
	"log/slog"
	"time"

	"github.com/paulsgrudups/testsync/internal/app"
	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"
)

// Server is the WebSocket endpoint agents register on.
type Server struct {
	Handler *CommandHandler

	// app carries the registry and the validator this server works against.
	// Both used to be package globals installed by startup, so a WebSocket
	// server could not be built for anything but the one process-wide
	// instance (CODE-1).
	app *app.App

	// pongWait overrides how long a connection may stay silent before its
	// reader gives up on the peer. Zero, the only value used in production,
	// means wsutil.PongWait; tests shorten it so that reaping an unresponsive
	// peer does not take half a minute.
	pongWait time.Duration
}

// NewServer builds the WebSocket endpoint for the given application. It
// starts nothing: [Server.RegisterRoutes] adds its route to the main router,
// so agents connect on the HTTP port through the same middleware as every
// other request (API-7).
func NewServer(a *app.App) *Server {
	return &Server{
		Handler: NewCommandHandler(a.Service, a.Metrics.Commands, a.Log),
		app:     a,
	}
}

// log returns the server's logger. A Server assembled without an App, which
// only happens in a test, discards rather than panicking.
func (s *Server) log() *slog.Logger {
	if s == nil || s.app == nil || s.app.Log == nil {
		return utils.DiscardLogger()
	}

	return s.app.Log
}

// pongWaitDuration returns the read deadline extension for a connection.
func (s *Server) pongWaitDuration() time.Duration {
	if s != nil && s.pongWait > 0 {
		return s.pongWait
	}

	return wsutil.PongWait
}
