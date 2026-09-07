// Package auth provides authentication middleware helpers.
package auth

import (
	"log/slog"
	"net/http"

	"github.com/paulsgrudups/testsync/utils"
)

// BasicAuthMiddleware validates requests using the provided validator.
//
// The validator is a parameter rather than a global resolved per request: a
// router cannot be built without one, so there is no registration order in
// which a route ends up unauthenticated (SEC-1). A nil validator denies every
// request rather than opening the route.
func BasicAuthMiddleware(
	v *Validator, logger *slog.Logger,
) func(http.Handler) http.Handler {
	if logger == nil {
		logger = utils.DiscardLogger()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveAuthorized(w, r, v, logger, next)
		})
	}
}

// serveAuthorized passes the request on only when the validator accepts it.
func serveAuthorized(
	w http.ResponseWriter, r *http.Request,
	v *Validator, logger *slog.Logger, next http.Handler,
) {
	if v.Disabled() {
		next.ServeHTTP(w, r)
		return
	}

	user, pass, ok := r.BasicAuth()
	if !ok || !v.Validate(user, pass) {
		logger.DebugContext(r.Context(), "request rejected: invalid or missing credentials",
			"method", r.Method, "path", r.URL.Path,
		)
		utils.HTTPError(w, "Request not authorized", http.StatusUnauthorized)

		return
	}

	next.ServeHTTP(w, r)
}
