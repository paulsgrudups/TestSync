package utils

import (
	"encoding/json"
	"net/http"
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
