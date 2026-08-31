package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/paulsgrudups/testsync/utils"
)

func newTestValidator(t *testing.T) *Validator {
	t.Helper()

	v, err := NewValidator(utils.BasicCredentials{Username: "user", Password: "pass"})
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	return v
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestBasicAuthMiddleware_Unauthorized(t *testing.T) {
	handler := BasicAuthMiddleware(newTestValidator(t))(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestBasicAuthMiddleware_Authorized(t *testing.T) {
	handler := BasicAuthMiddleware(newTestValidator(t))(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("user", "pass")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}

// TestBasicAuthMiddleware_WrongCredentials covers the HTTP half of SEC-1/SEC-2:
// neither half of the credential may be guessed independently.
func TestBasicAuthMiddleware_WrongCredentials(t *testing.T) {
	handler := BasicAuthMiddleware(newTestValidator(t))(okHandler())

	cases := []struct {
		name string
		user string
		pass string
	}{
		{name: "wrong username", user: "nope", pass: "pass"},
		{name: "wrong password", user: "user", pass: "nope"},
		{name: "empty credentials", user: "", pass: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.SetBasicAuth(tc.user, tc.pass)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
			}
		})
	}
}

// TestBasicAuthMiddleware_NilValidator covers the fail-closed default: a route
// wired up before credentials exist denies instead of serving everyone (SEC-1).
func TestBasicAuthMiddleware_NilValidator(t *testing.T) {
	handler := BasicAuthMiddleware(nil)(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("user", "pass")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

// TestBasicAuthMiddleware_Disabled covers the explicit opt-out.
func TestBasicAuthMiddleware_Disabled(t *testing.T) {
	handler := BasicAuthMiddleware(NewDisabledValidator())(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}

// TestSharedMiddleware checks that the middleware resolves the shared validator
// per request, so registration order cannot leave a route open (SEC-1).
func TestSharedMiddleware(t *testing.T) {
	previous := Shared()
	t.Cleanup(func() { SetShared(previous) })

	SetShared(nil)

	handler := SharedMiddleware()(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("user", "pass")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}

	SetShared(newTestValidator(t))

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}

	authorized := httptest.NewRequest(http.MethodGet, "/", nil)
	authorized.SetBasicAuth("user", "pass")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, authorized)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}
