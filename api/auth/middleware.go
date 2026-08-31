// Package auth provides authentication middleware helpers.
package auth

import (
	"net/http"

	"github.com/paulsgrudups/testsync/utils"

	log "github.com/sirupsen/logrus"
)

// BasicAuthMiddleware validates requests using provided validator.
func BasicAuthMiddleware(v *Validator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveAuthorized(w, r, v, next)
		})
	}
}

// SharedMiddleware validates requests using the process-wide validator
// installed by SetShared. The validator is resolved per request, so route
// registration order can never leave a route unauthenticated (SEC-1).
func SharedMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveAuthorized(w, r, Shared(), next)
		})
	}
}

// serveAuthorized passes the request on only when the validator accepts it.
func serveAuthorized(
	w http.ResponseWriter, r *http.Request, v *Validator, next http.Handler,
) {
	if v.Disabled() {
		next.ServeHTTP(w, r)
		return
	}

	user, pass, ok := r.BasicAuth()
	if !ok || !v.Validate(user, pass) {
		log.Debug("Request rejected: invalid or missing credentials")
		utils.HTTPError(w, "Request not authorized", http.StatusUnauthorized)

		return
	}

	next.ServeHTTP(w, r)
}
