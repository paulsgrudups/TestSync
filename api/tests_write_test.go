package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/internal/apptest"
	"github.com/paulsgrudups/testsync/utils"
)

// request performs one authenticated request against the agent-facing routes.
func request(
	t *testing.T, handler http.Handler, method, path string, body []byte,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.SetBasicAuth(apptest.Username, apptest.Password)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

// TestPutReplacesTestData covers the difference between PUT and POST: POST
// refuses a run that already has data, PUT is for replacing it, which is what
// a suite doing several passes over one run needs.
func TestPutReplacesTestData(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(t)

	if rec := request(t, handler, http.MethodPost, "/tests/200", []byte("first")); rec.Code != http.StatusOK {
		t.Fatalf("failed to store the first payload: %d (%s)", rec.Code, rec.Body.String())
	}

	// The route POST is refused on is exactly the one PUT exists for.
	if rec := request(t, handler, http.MethodPost, "/tests/200", []byte("second")); rec.Code != http.StatusConflict {
		t.Fatalf("expected POST to conflict, got %d", rec.Code)
	}

	rec := request(t, handler, http.MethodPut, "/tests/200", []byte("second"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	// PUT echoes what it stored, exactly as POST does.
	if rec.Body.String() != "second" {
		t.Fatalf("expected the stored body echoed back, got %q", rec.Body.String())
	}

	if read := request(t, handler, http.MethodGet, "/tests/200", nil); read.Body.String() != "second" {
		t.Fatalf("expected the replacement to be stored, got %q", read.Body.String())
	}
}

// TestPutStoresDataForAnUnseenRun covers PUT on a test ID nothing has written
// yet: replacing nothing is still storing.
func TestPutStoresDataForAnUnseenRun(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(t)

	rec := request(t, handler, http.MethodPut, "/tests/201", []byte("payload"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	if read := request(t, handler, http.MethodGet, "/tests/201", nil); read.Body.String() != "payload" {
		t.Fatalf("expected the payload to be stored, got %q", read.Body.String())
	}
}

// TestPutRejectsOversizedBody covers limits.max_data_bytes on the new write
// path. A cap that only some of the write routes enforce is not a cap, so PUT
// answers exactly as POST does and stores nothing.
func TestPutRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	handler := newLimitedRouter(
		t, newLimitedApp(t, utils.LimitsConfig{MaxDataBytes: 16}),
	)

	rec := request(t, handler, http.MethodPut, "/tests/202", make([]byte, 17))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf(
			"expected status %d, got %d", http.StatusRequestEntityTooLarge, rec.Code,
		)
	}

	assertErrorBody(t, rec, http.StatusRequestEntityTooLarge)

	if read := request(t, handler, http.MethodGet, "/tests/202", nil); read.Code != http.StatusNotFound {
		t.Fatalf("a rejected payload was stored: status %d", read.Code)
	}
}

// TestDeleteTestRemovesTheRunAndItsData covers the agent-facing spelling of
// the delete. It is the same operation as the operator-facing one, so it
// answers the same way: 204, the dropped-agent count, and nothing left behind.
func TestDeleteTestRemovesTheRunAndItsData(t *testing.T) {
	t.Parallel()

	application := apptest.NewDefault(t)

	handler, err := NewRouter(application)
	if err != nil {
		t.Fatalf("failed to create the router: %v", err)
	}

	if rec := request(t, handler, http.MethodPost, "/tests/203", []byte("payload")); rec.Code != http.StatusOK {
		t.Fatalf("failed to store the payload: %d (%s)", rec.Code, rec.Body.String())
	}

	rec := request(t, handler, http.MethodDelete, "/tests/203", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}

	if rec.Body.Len() != 0 {
		t.Fatalf("expected no body, got %q", rec.Body.String())
	}

	if got := rec.Header().Get(runs.ConnectionsDroppedHeader); got != "0" {
		t.Fatalf("expected no dropped connections, got %q", got)
	}

	if _, ok := application.Registry.Get(203); ok {
		t.Fatal("the run is still registered")
	}

	if read := request(t, handler, http.MethodGet, "/tests/203", nil); read.Code != http.StatusNotFound {
		t.Fatalf("the payload outlived the run: status %d", read.Code)
	}

	// Deleting what is already gone is a 404, not a silent success: an
	// operator retrying a delete needs to be able to tell the two apart.
	again := request(t, handler, http.MethodDelete, "/tests/203", nil)
	if again.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, again.Code)
	}

	if !strings.Contains(again.Body.String(), runs.ErrorReasonRunNotFound) {
		t.Fatalf("expected the run_not_found reason, got %s", again.Body.String())
	}
}

// TestWriteRoutesRequireCredentials covers SEC-1 for the two new agent-facing
// verbs: neither may act without credentials.
func TestWriteRoutesRequireCredentials(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(t)

	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/tests/204", strings.NewReader("payload"))

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
			}
		})
	}
}
