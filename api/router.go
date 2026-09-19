// Package api provides HTTP API handlers.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/paulsgrudups/testsync/api/monitor"
	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/internal/app"
	"github.com/paulsgrudups/testsync/utils"

	"github.com/gorilla/mux"
)

// NewRouter builds the HTTP handler for one application. Everything it needs
// arrives on the App: there is no package state to install first and no order
// in which the routes can be registered before they are authenticated
// (CODE-1, SEC-1).
func NewRouter(a *app.App) (http.Handler, error) {
	router := mux.NewRouter().StrictSlash(false)

	if err := registerMiddlewares(router, a); err != nil {
		return nil, fmt.Errorf("failed to register middlewares: %w", err)
	}

	registerOperationalRoutes(router, a)

	runs.RegisterTestsRoutes(router, a.Service, a.Auth, a.Log)

	// Monitoring, the operator overrides and the UI page, behind the same
	// validator.
	monitor.RegisterRoutes(router, a)

	return router, nil
}

func registerMiddlewares(r *mux.Router, a *app.App) error {
	body, err := json.Marshal(utils.ErrorResponse{
		Code:  http.StatusServiceUnavailable,
		Error: "Request timed out",
	})
	if err != nil {
		return fmt.Errorf("failed to marshal the timeout body: %w", err)
	}

	timeoutMW := func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, 10*time.Second, string(body))
	}

	// Outermost, so a request is counted with the status its client actually
	// received: the 500 of a recovered panic, or the 503 of a timeout.
	r.Use(utils.CountRequests(a.Metrics.Requests))
	// Outside the timeout, so that it also catches the panic
	// http.TimeoutHandler re-raises in this goroutine after its own handler
	// goroutine panicked.
	r.Use(utils.RecoverPanics(a.Log))
	r.Use(timeoutMW)
	r.Use(utils.LogRequests(a.Log))

	return nil
}
