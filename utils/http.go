package utils

import (
	"encoding/json"
	"errors"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// ErrorResponse will be sent in case an error occurs during request processing.
type ErrorResponse struct {
	// Status code of error
	Code int `json:"code"`
	// Error description
	Error string `json:"error"`
}

type responseWriter struct {
	http.ResponseWriter

	statusCode int
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{w, http.StatusOK}
}

// WriteHeader overrides default WriteHeader. Response code is saved for logging
// purposes.
func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// LogRequests returns handler function that processes all incoming HTTP
// requests all requests are logged to specified file.
func LogRequests(next http.Handler) http.Handler {
	handler := func(w http.ResponseWriter, r *http.Request) {
		rw := newResponseWriter(w)
		start := time.Now()

		next.ServeHTTP(rw, r)

		reqPath, _, _ := strings.Cut(r.RequestURI, "?")
		if len(strings.Split(r.RequestURI, "?")) > 1 {
			reqPath += "?"
		}

		log.Infof(
			"[%s] %s:\t%s  - %d",
			time.Since(start), r.Method, reqPath, rw.statusCode,
		)
	}

	return http.HandlerFunc(handler)
}

// RecoverPanics returns a handler that turns a panic in any handler below it
// into a logged stack trace and a 500 response. net/http recovers panics in the
// handler goroutine, but [http.TimeoutHandler] re-panics them in the caller's
// goroutine, so the server needs its own net.
func RecoverPanics(next http.Handler) http.Handler {
	handler := func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}

			// net/http's own signal for "abort this connection quietly".
			if recErr, ok := rec.(error); ok && errors.Is(recErr, http.ErrAbortHandler) {
				panic(rec)
			}

			log.Errorf(
				"Recovered panic while serving %s %s: %v\n%s",
				r.Method, r.URL.Path, rec, debug.Stack(),
			)

			HTTPError(w, "Internal server error", http.StatusInternalServerError)
		}()

		next.ServeHTTP(w, r)
	}

	return http.HandlerFunc(handler)
}

// RecoverGoroutine recovers a panic in the goroutine it is deferred in and logs
// it with a stack trace, so that one connection's bug cannot take down the
// process and every other agent's run with it. Defer it as the first statement
// of every spawned goroutine, so that it runs after the goroutine's own
// cleanup.
func RecoverGoroutine(name string) {
	if rec := recover(); rec != nil {
		log.Errorf(
			"Recovered panic in %s goroutine: %v\n%s", name, rec, debug.Stack(),
		)
	}
}

// HTTPError writes Loadero's default error response.
func HTTPError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(code)

	// write error response and log if it fails
	if err := json.NewEncoder(w).Encode(ErrorResponse{
		Code:  code,
		Error: message,
	}); err != nil {
		log.Debugf("failed to write error response: %v", err)
	}
}
