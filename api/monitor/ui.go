package monitor

import (
	_ "embed"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/paulsgrudups/testsync/api/auth"

	log "github.com/sirupsen/logrus"
)

// contentSecurityPolicy keeps the page self-contained. Nothing is fetched from
// anywhere but this server, which is what makes the UI usable on an air-gapped
// CI box and removes the whole class of "the dashboard leaked the run" bugs.
const contentSecurityPolicy = "default-src 'none'; " +
	"script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; form-action 'none'; base-uri 'none'; " +
	"frame-ancestors 'none'"

// The page is embedded so that the server stays a single binary with no asset
// directory to deploy alongside it.
var (
	//go:embed assets/index.html
	indexHTML []byte

	//go:embed assets/app.css
	appCSS []byte

	//go:embed assets/app.js
	appJS []byte
)

// registerUIRoutes serves the operator page and its two assets. They are
// listed one by one rather than served from a file server: there is no
// directory listing, no path to traverse and no guessing about content types.
func registerUIRoutes(r *mux.Router) {
	uiRouter := r.PathPrefix(UIPrefix).Subrouter().StrictSlash(false)
	uiRouter.Use(challengeUnauthorized, auth.SharedMiddleware())

	page := assetHandler("text/html; charset=UTF-8", indexHTML)

	uiRouter.HandleFunc("", page).Methods(http.MethodGet)
	uiRouter.HandleFunc("/", page).Methods(http.MethodGet)
	uiRouter.HandleFunc(
		"/app.css", assetHandler("text/css; charset=UTF-8", appCSS),
	).Methods(http.MethodGet)
	uiRouter.HandleFunc(
		"/app.js", assetHandler("text/javascript; charset=UTF-8", appJS),
	).Methods(http.MethodGet)
}

// assetHandler serves one embedded asset.
func assetHandler(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		if _, err := w.Write(body); err != nil {
			log.Debugf("failed to write monitoring asset: %v", err)
		}
	}
}

// challengeUnauthorized turns the shared middleware's 401 into a Basic auth
// challenge, so a browser opening /ui asks for credentials instead of showing
// a bare error. It changes nothing about who is let in: the one shared
// validator still decides that, and this wrapper only ever adds a header.
func challengeUnauthorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&challengeWriter{ResponseWriter: w}, r)
	})
}

// challengeWriter adds the WWW-Authenticate header to a 401 response.
type challengeWriter struct {
	http.ResponseWriter

	wroteHeader bool
}

// WriteHeader adds the challenge before the status line is committed.
func (w *challengeWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true

		if code == http.StatusUnauthorized {
			w.Header().Set(
				"WWW-Authenticate", `Basic realm="TestSync", charset="UTF-8"`,
			)
		}
	}

	w.ResponseWriter.WriteHeader(code)
}
