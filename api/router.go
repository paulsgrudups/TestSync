// Package api provides HTTP API handlers.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/utils"

	"github.com/gorilla/mux"
	"github.com/pkg/errors"

	log "github.com/sirupsen/logrus"
)

// HandleRoutes registers all routes.
func HandleRoutes() (http.Handler, error) {
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

	runs.RegisterTestsRoutes(router)

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

	r.Use(timeoutMW)
	r.Use(utils.LogRequests)

	return nil
}
