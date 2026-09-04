package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paulsgrudups/testsync/internal/app"
	"github.com/paulsgrudups/testsync/internal/apptest"
	"github.com/paulsgrudups/testsync/utils"
)

// newLimitedApp builds an application whose limits are the operator's, so a
// limit test exercises the same path a configured server takes.
func newLimitedApp(t *testing.T, limits utils.LimitsConfig) *app.App {
	t.Helper()

	conf := apptest.Config()
	conf.Limits = limits

	return apptest.New(t, conf)
}

// newLimitedRouter builds the real router over that application.
func newLimitedRouter(t *testing.T, application *app.App) http.Handler {
	t.Helper()

	handler, err := NewRouter(application)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	return handler
}

// TestPostRejectsOversizedBody covers the documented rejection for
// limits.max_data_bytes on the HTTP path: 413, with the standard JSON error
// body, and nothing stored.
func TestPostRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	handler := newLimitedRouter(
		t, newLimitedApp(t, utils.LimitsConfig{MaxDataBytes: 16}),
	)

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
	t.Parallel()

	application := newLimitedApp(t, utils.LimitsConfig{MaxTests: 1})
	handler := newLimitedRouter(t, application)

	if _, err := application.Registry.Ensure(1); err != nil {
		t.Fatalf("failed to register the first run: %v", err)
	}

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

	if _, ok := application.Registry.Get(2); ok {
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
