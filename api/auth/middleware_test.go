package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/paulsgrudups/testsync/utils"
)

func TestBasicAuthMiddleware_Unauthorized(t *testing.T) {
	validator := NewValidator(utils.BasicCredentials{Username: "user", Password: "pass"})
	handler := BasicAuthMiddleware(validator)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestBasicAuthMiddleware_Authorized(t *testing.T) {
	validator := NewValidator(utils.BasicCredentials{Username: "user", Password: "pass"})
	handler := BasicAuthMiddleware(validator)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("user", "pass")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}
