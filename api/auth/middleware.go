package auth

import (
	"net/http"

	"github.com/paulsgrudups/testsync/utils"
)

// BasicAuthMiddleware validates requests using provided validator.
func BasicAuthMiddleware(v *Validator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()
			if !ok || v == nil || !v.Validate(user, pass) {
				utils.HTTPError(w, "Request not authorized", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
