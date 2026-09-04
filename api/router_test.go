package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

// TestRouterRecoversHandlerPanic covers the HTTP router's half of STAB-1: a
// panicking handler is answered with a 500 instead of unwinding into the
// server's connection goroutine.
func TestRouterRecoversHandlerPanic(t *testing.T) {
	t.Parallel()

	router := mux.NewRouter()
	if err := registerMiddlewares(router); err != nil {
		t.Fatalf("failed to register middlewares: %v", err)
	}

	router.HandleFunc("/panic", func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
}
