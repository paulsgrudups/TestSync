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
	"github.com/pkg/errors"

	log "github.com/sirupsen/logrus"
)

// NewRouter builds the HTTP handler for one application. Everything it needs
// arrives on the App: there is no package state to install first and no order
// in which the routes can be registered before they are authenticated
// (CODE-1, SEC-1).
func NewRouter(a *app.App) (http.Handler, error) {
	router := mux.NewRouter().StrictSlash(false)

	err := registerMiddlewares(router)
	if err != nil {
		return nil, errors.Wrap(err, "failed to register middlewares")
	}

	router.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := fmt.Fprintln(w, "A random proverb that is very intellectual."); err != nil {
			log.Debugf("failed to write root response: %v", err)
		}
	})

	router.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`)) // nolint: gosec, errcheck
	})

	runs.RegisterTestsRoutes(router, a.Service, a.Auth)

	// Read-only monitoring API and operator page, behind the same validator.
	monitor.RegisterRoutes(router, a.Registry, a.Auth)

	return router, nil
}

func registerMiddlewares(r *mux.Router) error {
	body, err := json.Marshal(utils.ErrorResponse{
		Code:  http.StatusServiceUnavailable,
		Error: "Request timed out",
	})
	if err != nil {
		return errors.Wrap(err, "failed to marshal timeout body")
	}

	timeoutMW := func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, 10*time.Second, string(body))
	}

	// Outermost, so that it also catches the panic http.TimeoutHandler
	// re-raises in this goroutine after its own handler goroutine panicked.
	r.Use(utils.RecoverPanics)
	r.Use(timeoutMW)
	r.Use(utils.LogRequests)

	return nil
}
