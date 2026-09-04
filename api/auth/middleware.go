// Package auth provides authentication middleware helpers.
package auth

import (
	"net/http"

	"github.com/paulsgrudups/testsync/utils"

	log "github.com/sirupsen/logrus"
)

// BasicAuthMiddleware validates requests using the provided validator.
//
// The validator is a parameter rather than a global resolved per request: a
// router cannot be built without one, so there is no registration order in
// which a route ends up unauthenticated (SEC-1). A nil validator denies every
// request rather than opening the route.
func BasicAuthMiddleware(v *Validator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveAuthorized(w, r, v, next)
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
