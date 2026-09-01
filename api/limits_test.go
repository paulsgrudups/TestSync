package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/internal/storagetest"
	"github.com/paulsgrudups/testsync/utils"
)

// withLimits installs limits for one test and restores them afterwards.
func withLimits(t *testing.T, limits runs.Limits) {
	t.Helper()

	previous := runs.CurrentLimits()
	t.Cleanup(func() { runs.SetLimits(previous) })

	runs.SetLimits(limits)
}

// newLimitedRouter builds the real router over a fresh registry and store.
func newLimitedRouter(t *testing.T) http.Handler {
	t.Helper()

	installTestCredentials(t)

	runs.AllTests = make(map[int]*runs.Test)
	t.Cleanup(func() { runs.AllTests = make(map[int]*runs.Test) })

	runs.SetDataStore(storagetest.NewStore(t))

	handler, err := HandleRoutes()
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	return handler
}

// TestPostRejectsOversizedBody covers the documented rejection for
// limits.max_data_bytes on the HTTP path: 413, with the standard JSON error
// body, and nothing stored.
func TestPostRejectsOversizedBody(t *testing.T) {
	limits := runs.DefaultLimits()
	limits.MaxDataBytes = 16
	withLimits(t, limits)

	handler := newLimitedRouter(t)

	req := httptest.NewRequest(
		http.MethodPost, "/tests/1", bytes.NewReader(make([]byte, 17)),
	)
	req.SetBasicAuth("user", "pass")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf(
			"expected status %d, got %d", http.StatusRequestEntityTooLarge, rec.Code,
		)
	}

	assertErrorBody(t, rec, http.StatusRequestEntityTooLarge)

	get := httptest.NewRequest(http.MethodGet, "/tests/1", nil)
	get.SetBasicAuth("user", "pass")

	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, get)

	if getRec.Code != http.StatusNotFound {
		t.Fatalf("a rejected payload was stored: status %d", getRec.Code)
	}
}

// TestPostRejectsNewRunsPastTheLimit covers the documented rejection for
// limits.max_tests on the HTTP path: 503, so a client can tell "the server is
// full" from "your request was wrong".
func TestPostRejectsNewRunsPastTheLimit(t *testing.T) {
	limits := runs.DefaultLimits()
	limits.MaxTests = 1
	withLimits(t, limits)

	handler := newLimitedRouter(t)

	runs.SetTest(1, &runs.Test{Created: time.Now()})

	req := httptest.NewRequest(
		http.MethodPost, "/tests/2", strings.NewReader("payload"),
	)
	req.SetBasicAuth("user", "pass")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"expected status %d, got %d", http.StatusServiceUnavailable, rec.Code,
		)
	}

	assertErrorBody(t, rec, http.StatusServiceUnavailable)

	if _, ok := runs.GetTest(2); ok {
		t.Fatal("a refused run was registered anyway")
	}
}

// assertErrorBody checks that a rejection uses the documented error shape.
func assertErrorBody(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()

	var body utils.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("rejection body is not JSON: %v (%q)", err, rec.Body.String())
	}

	if body.Code != code {
		t.Fatalf("expected code %d in the body, got %d", code, body.Code)
	}

	if body.Error == "" {
		t.Fatal("rejection body carries no message")
	}
}
