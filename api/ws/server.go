package ws

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Server describes WebSocket server with available handler functions
type Server struct {
	HTTPServer *http.Server
	Handler    *CommandHandler
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
	go s.HTTPServer.ListenAndServe() // nolint: errcheck

	return s
}

// Shutdown gracefully stops the WebSocket server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.HTTPServer == nil {
		return nil
	}

	return s.HTTPServer.Shutdown(ctx)
}
