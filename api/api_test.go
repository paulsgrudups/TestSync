// Package api contains tests for the API handlers.
package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/internal/storagetest"
	"github.com/paulsgrudups/testsync/utils"
)

// installTestCredentials points the shared validator, which both the HTTP and
// the WebSocket server authenticate through, at a known credential (SEC-1).
func installTestCredentials(t *testing.T) {
	t.Helper()

	validator, err := auth.NewValidator(
		utils.BasicCredentials{Username: "user", Password: "pass"},
	)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	previous := auth.Shared()
	t.Cleanup(func() { auth.SetShared(previous) })
	auth.SetShared(validator)
}

func TestCreateAndReadTestData(t *testing.T) {
	installTestCredentials(t)

	runs.AllTests = make(map[int]*runs.Test)

	runs.SetDataStore(storagetest.NewStore(t))

	handler, err := HandleRoutes()
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	postReq := httptest.NewRequest(http.MethodPost, "/tests/123", strings.NewReader("payload"))
	postReq.SetBasicAuth("user", "pass")
	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, postRec.Code)
	}

	if postRec.Body.String() != "payload" {
		t.Fatalf("unexpected body: %q", postRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/tests/123", nil)
	getReq.SetBasicAuth("user", "pass")
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, getRec.Code)
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
	installTestCredentials(t)

	runs.SetDataStore(storagetest.NewStore(t))

	handler, err := HandleRoutes()
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

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
	previous := auth.Shared()
	t.Cleanup(func() { auth.SetShared(previous) })
	auth.SetShared(nil)

	runs.SetDataStore(storagetest.NewStore(t))

	handler, err := HandleRoutes()
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
