package utils

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/paulsgrudups/testsync/internal/metrics"
)

// ErrorResponse will be sent in case an error occurs during request processing.
type ErrorResponse struct {
	// Status code of error
	Code int `json:"code"`
	// Error description
	Error string `json:"error"`
	// Reason is a stable, machine-readable name for the failure, such as
	// "run_not_found". It lets a client branch on the cause without matching
	// on the prose in Error, which is free to change.
	//
	// It is omitted when empty, so a response written by [HTTPError] is
	// byte-identical to what it has always been.
	Reason string `json:"reason,omitempty"`
}

type responseWriter struct {
	http.ResponseWriter

	statusCode int
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{w, http.StatusOK}
}

// Hijack hands the connection over, which is what a WebSocket upgrade does.
// gorilla/websocket asserts [http.Hijacker] on the writer it is given, so a
// wrapper without this method would refuse every upgrade behind it. The
// status is recorded as the 101 the upgrade answered with.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buf, err := http.NewResponseController(rw.ResponseWriter).Hijack()
	if err == nil {
		rw.statusCode = http.StatusSwitchingProtocols
	}

	return conn, buf, err
}

// Unwrap exposes the wrapped writer to [http.ResponseController].
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// WriteHeader overrides default WriteHeader. Response code is saved for logging
// purposes.
func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// LogRequests returns middleware that logs one line per request through the
// given logger. The query string is dropped: credentials may still arrive in
// one on the deprecated WebSocket path (SEC-3), and a log file is exactly
// where they must not end up.
func LogRequests(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := newResponseWriter(w)
			start := time.Now()

			next.ServeHTTP(rw, r)

			reqPath, _, hadQuery := strings.Cut(r.RequestURI, "?")
			if hadQuery {
				reqPath += "?"
			}

			logger.InfoContext(r.Context(), "request",
				"method", r.Method,
				"path", reqPath,
				"status", rw.statusCode,
				"duration", time.Since(start).String(),
			)
		})
	}
}

// routePattern matches a mux path variable with its pattern, {testID:\d+}, so
// that a route label reads {testID}.
var routePattern = regexp.MustCompile(`\{([^:}]+):(?:[^{}]|\{[^{}]*\})*\}`)

// CountRequests returns middleware that counts every request it sees by the
// matched route's template and the response status. The template, never the
// raw path, is the label: a path carries a test ID, and a label per test ID
// is a series per test run.
func CountRequests(counter *metrics.CounterVec) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := newResponseWriter(w)

			next.ServeHTTP(rw, r)

			route := "unmatched"
			if current := mux.CurrentRoute(r); current != nil {
				if template, err := current.GetPathTemplate(); err == nil {
					route = routePattern.ReplaceAllString(template, "{$1}")
				}
			}

			counter.Inc(route, strconv.Itoa(rw.statusCode))
		})
	}
}

// RecoverPanics returns a handler that turns a panic in any handler below it
// into a logged stack trace and a 500 response. net/http recovers panics in the
// handler goroutine, but [http.TimeoutHandler] re-panics them in the caller's
// goroutine, so the server needs its own net.
func RecoverPanics(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}

				// net/http's own signal for "abort this connection quietly".
				if recErr, ok := rec.(error); ok && errors.Is(recErr, http.ErrAbortHandler) {
					panic(rec)
				}

				logger.ErrorContext(r.Context(), "recovered panic while serving a request",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(rec),
					"stack", string(debug.Stack()),
				)

				HTTPError(w, "Internal server error", http.StatusInternalServerError)
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// RecoverGoroutine recovers a panic in the goroutine it is deferred in and logs
// it with a stack trace, so that one connection's bug cannot take down the
// process and every other agent's run with it. Defer it as the first statement
// of every spawned goroutine, so that it runs after the goroutine's own
// cleanup.
func RecoverGoroutine(logger *slog.Logger, name string) {
	if rec := recover(); rec != nil {
		logger.Error("recovered panic in a goroutine",
			"goroutine", name,
			"panic", fmt.Sprint(rec),
			"stack", string(debug.Stack()),
		)
	}
}

// HTTPError writes the server's standard JSON error response.
//
// A failed write is deliberately ignored rather than logged: it means the
// client is already gone, there is nothing left to tell it, and the request
// logging middleware has already recorded the status. Logging it would need a
// logger at every one of this function's call sites to say nothing useful.
func HTTPError(w http.ResponseWriter, message string, code int) {
	HTTPErrorReason(w, message, code, "")
}

// HTTPErrorReason writes the standard JSON error response with a stable
// machine-readable reason alongside the human-readable message, so a client
// can branch on the cause rather than on the prose. An empty reason is
// omitted, which is what makes [HTTPError] a special case of this.
//
// A failed write is ignored for the same reason it is in [HTTPError].
func HTTPErrorReason(w http.ResponseWriter, message string, code int, reason string) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(code)

	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Code:   code,
		Error:  message,
		Reason: reason,
	})
}
