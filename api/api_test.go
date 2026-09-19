package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paulsgrudups/testsync/internal/apptest"
)

// newTestRouter builds a router over an application of this test's own, so
// nothing it does is visible to any other test (TEST-2).
func newTestRouter(t *testing.T) http.Handler {
	t.Helper()

	handler, err := NewRouter(apptest.NewDefault(t))
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	return handler
}

func TestCreateAndReadTestData(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(t)

	postReq := httptest.NewRequest(http.MethodPost, "/tests/123", strings.NewReader("payload"))
	postReq.SetBasicAuth("user", "pass")
	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, postReq)

	// 201 with the new resource's location, and a short receipt rather than
	// the payload echoed back (API-4).
	if postRec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, postRec.Code)
	}

	if got := postRec.Header().Get("Location"); got != "/tests/123" {
		t.Fatalf("expected Location /tests/123, got %q", got)
	}

	if got := postRec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("unexpected create Content-Type %q", got)
	}

	if want := `{"test_id":123,"bytes":7}`; postRec.Body.String() != want {
		t.Fatalf("unexpected body: %q, want %q", postRec.Body.String(), want)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/tests/123", nil)
	getReq.SetBasicAuth("user", "pass")
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, getRec.Code)
	}

	// The payload is opaque, and said to be, rather than left to sniffing.
	if got := getRec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("unexpected read Content-Type %q", got)
	}

	if got := getRec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff, got %q", got)
	}

	read, err := io.ReadAll(getRec.Body)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}
	if string(read) != "payload" {
		t.Fatalf("unexpected body: %q", string(read))
	}
}

// TestTestsRoutesRejectBadCredentials covers the HTTP half of SEC-1/SEC-2 on
// the real router: neither half of the credential may be guessed on its own,
// and a request without credentials never reaches a handler.
func TestTestsRoutesRejectBadCredentials(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(t)

	cases := []struct {
		name     string
		user     string
		pass     string
		withAuth bool
	}{
		{name: "no credentials"},
		{name: "wrong username", user: "nope", pass: "pass", withAuth: true},
		{name: "wrong password", user: "user", pass: "nope", withAuth: true},
		{name: "empty credentials", user: "", pass: "", withAuth: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/tests/321", nil)
			if tc.withAuth {
				req.SetBasicAuth(tc.user, tc.pass)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
			}
		})
	}
}

// TestTestsRoutesDenyWithoutValidator covers the fail-closed default: a process
// that never installed a validator serves nobody (SEC-1).
func TestTestsRoutesDenyWithoutValidator(t *testing.T) {
	t.Parallel()

	handler, err := NewRouter(
		apptest.WithValidator(t, apptest.Config(), nil),
	)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tests/321", nil)
	req.SetBasicAuth("user", "pass")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}
