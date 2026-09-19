package monitor

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/utils"
)

// do performs an authenticated request against the management routes.
func do(
	t *testing.T, handler http.Handler, method, path, body string,
) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req := httptest.NewRequest(method, path, reader)
	req.SetBasicAuth("user", "pass")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

// errorBody decodes the standard error response.
func errorBody(t *testing.T, rec *httptest.ResponseRecorder) utils.ErrorResponse {
	t.Helper()

	var body utils.ErrorResponse

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode the error body %q: %v", rec.Body.String(), err)
	}

	return body
}

// expectError asserts a status and the stable reason that goes with it.
func expectError(
	t *testing.T, rec *httptest.ResponseRecorder, status int, reason string,
) {
	t.Helper()

	if rec.Code != status {
		t.Fatalf("expected status %d, got %d (%s)", status, rec.Code, rec.Body.String())
	}

	if body := errorBody(t, rec); body.Reason != reason {
		t.Fatalf("expected reason %q, got %q (%s)", reason, body.Reason, rec.Body.String())
	}
}

// TestManagementRoutesRequireCredentials covers SEC-1 for the routes that
// change something: an unauthenticated caller must not be able to release a
// barrier, drop an agent, delete a run or read a payload.
func TestManagementRoutesRequireCredentials(t *testing.T) {
	t.Parallel()

	handler, _ := newTestRouter(t)

	requests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/runs/1/checkpoints/release"},
		{http.MethodDelete, "/api/v1/runs/1"},
		{http.MethodDelete, "/api/v1/runs/1/connections/2"},
		{http.MethodGet, "/api/v1/runs/1/data"},
	}

	for _, req := range requests {
		t.Run(req.method+" "+req.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(req.method, req.path, strings.NewReader("{}")))

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
			}
		})
	}
}

// TestReleaseCheckpointEndpoint covers the force-release: the waiting agents
// are counted, the round that ended is named, and the barrier is left idle in
// its next round.
func TestReleaseCheckpointEndpoint(t *testing.T) {
	t.Parallel()

	handler, application := newTestRouter(t)

	run, ids := newRun(t, application, 100, 3)
	joinCheckpoint(t, run, "checkout-flow", 3, ids[0])
	joinCheckpoint(t, run, "checkout-flow", 3, ids[1])

	rec := do(t, handler, http.MethodPost, "/api/v1/runs/100/checkpoints/release",
		`{"identifier":"checkout-flow"}`,
	)

	var body struct {
		Identifier string `json:"identifier"`
		Reason     string `json:"reason"`
		Generation int    `json:"generation"`
		Released   int    `json:"released"`
		Target     int    `json:"target"`
	}

	decode(t, rec, &body)

	if body.Identifier != "checkout-flow" || body.Reason != runs.ReasonOperatorReleased {
		t.Fatalf("unexpected release body: %+v", body)
	}

	if body.Generation != 1 || body.Released != 2 || body.Target != 3 {
		t.Fatalf("unexpected release body: %+v", body)
	}

	var detail struct {
		Run         runSummaryDTO `json:"run"`
		Checkpoints []struct {
			Identifier string `json:"identifier"`
			Generation int    `json:"generation"`
			Waiting    bool   `json:"waiting"`
		} `json:"checkpoints"`
	}

	decode(t, get(t, handler, "/api/v1/runs/100"), &detail)

	if detail.Run.Waiting {
		t.Fatalf("the run is still reported as waiting: %+v", detail.Run)
	}

	if len(detail.Checkpoints) != 1 || detail.Checkpoints[0].Generation != 2 {
		t.Fatalf("expected the barrier to have moved on, got %+v", detail.Checkpoints)
	}
}

// TestReleaseCheckpointAcceptsAnOperatorReason covers the optional reason,
// which is what an operator leaves behind for whoever reads the agent logs.
func TestReleaseCheckpointAcceptsAnOperatorReason(t *testing.T) {
	t.Parallel()

	handler, application := newTestRouter(t)

	run, ids := newRun(t, application, 101, 2)
	joinCheckpoint(t, run, "gate", 2, ids[0])

	rec := do(t, handler, http.MethodPost, "/api/v1/runs/101/checkpoints/release",
		`{"identifier":"gate","reason":"agent 2 lost its runner"}`,
	)

	var body struct {
		Reason string `json:"reason"`
	}

	decode(t, rec, &body)

	if body.Reason != "agent 2 lost its runner" {
		t.Fatalf("expected the operator's reason, got %q", body.Reason)
	}
}

// TestReleaseCheckpointRejectsBadRequests covers the 400s: the identifier is
// required, and a reason that would be echoed to every agent is bounded.
func TestReleaseCheckpointRejectsBadRequests(t *testing.T) {
	t.Parallel()

	handler, application := newTestRouter(t)

	run, ids := newRun(t, application, 102, 2)
	joinCheckpoint(t, run, "gate", 2, ids[0])

	oversized := `{"identifier":"gate","reason":"` +
		strings.Repeat("x", runs.MaxReleaseReasonBytes+1) + `"}`

	bodies := map[string]string{
		"no identifier":     `{"reason":"whatever"}`,
		"blank identifier":  `{"identifier":"   "}`,
		"malformed json":    `{"identifier":`,
		"empty body":        ``,
		"oversized reason":  oversized,
		"identifier is int": `{"identifier":7}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := do(t, handler, http.MethodPost,
				"/api/v1/runs/102/checkpoints/release", body,
			)

			expectError(t, rec, http.StatusBadRequest, runs.ErrorReasonInvalidRequest)
		})
	}

	// Nothing was released by any of them.
	var detail struct {
		Run runSummaryDTO `json:"run"`
	}

	decode(t, get(t, handler, "/api/v1/runs/102"), &detail)

	if !detail.Run.Waiting {
		t.Fatal("a rejected request released the round anyway")
	}
}

// TestReleaseCheckpointFailures covers the three refusals that are not about
// the request itself, each with its own status.
func TestReleaseCheckpointFailures(t *testing.T) {
	t.Parallel()

	handler, application := newTestRouter(t)

	run, ids := newRun(t, application, 103, 1)

	// A round of one releases as soon as it is joined, leaving the barrier
	// idle between rounds.
	joinCheckpoint(t, run, "done", 1, ids[0])

	tests := map[string]struct {
		path   string
		body   string
		status int
		reason string
	}{
		"unknown run": {
			path:   "/api/v1/runs/999/checkpoints/release",
			body:   `{"identifier":"done"}`,
			status: http.StatusNotFound,
			reason: runs.ErrorReasonRunNotFound,
		},
		"unknown checkpoint": {
			path:   "/api/v1/runs/103/checkpoints/release",
			body:   `{"identifier":"never-seen"}`,
			status: http.StatusNotFound,
			reason: runs.ErrorReasonCheckpointNotFound,
		},
		"idle checkpoint": {
			path:   "/api/v1/runs/103/checkpoints/release",
			body:   `{"identifier":"done"}`,
			status: http.StatusConflict,
			reason: runs.ErrorReasonNoRoundInProgress,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			expectError(
				t, do(t, handler, http.MethodPost, tc.path, tc.body),
				tc.status, tc.reason,
			)
		})
	}
}

// TestDisconnectEndpoint covers dropping one agent: it stops being reported on
// the run, and the ID cannot be dropped twice.
func TestDisconnectEndpoint(t *testing.T) {
	t.Parallel()

	handler, application := newTestRouter(t)

	_, ids := newRun(t, application, 104, 3)

	path := "/api/v1/runs/104/connections/" + connIDPath(ids[1])

	rec := do(t, handler, http.MethodDelete, path, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}

	if rec.Body.Len() != 0 {
		t.Fatalf("expected no body, got %q", rec.Body.String())
	}

	var detail struct {
		Connections []struct {
			ConnID runs.ConnID `json:"conn_id"`
		} `json:"connections"`
	}

	decode(t, get(t, handler, "/api/v1/runs/104"), &detail)

	if len(detail.Connections) != 2 {
		t.Fatalf("expected 2 connections left, got %d", len(detail.Connections))
	}

	for _, conn := range detail.Connections {
		if conn.ConnID == ids[1] {
			t.Fatalf("the disconnected agent is still reported: %+v", detail.Connections)
		}
	}

	expectError(
		t, do(t, handler, http.MethodDelete, path, ""),
		http.StatusNotFound, runs.ErrorReasonConnectionNotFound,
	)

	expectError(
		t, do(t, handler, http.MethodDelete, "/api/v1/runs/998/connections/1", ""),
		http.StatusNotFound, runs.ErrorReasonRunNotFound,
	)
}

// TestDeleteRunEndpoint covers deleting a run and its payload together, and
// the header that warns an operator they interrupted a live suite.
func TestDeleteRunEndpoint(t *testing.T) {
	t.Parallel()

	handler, application := newTestRouter(t)

	newRun(t, application, 105, 2)

	if err := application.Service.UpdateTestData(
		t.Context(), 105, []byte("payload"),
	); err != nil {
		t.Fatalf("failed to store the payload: %v", err)
	}

	rec := do(t, handler, http.MethodDelete, "/api/v1/runs/105", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}

	if got := rec.Header().Get("X-TestSync-Connections-Dropped"); got != "2" {
		t.Fatalf("expected 2 dropped connections in the header, got %q", got)
	}

	if detail := get(t, handler, "/api/v1/runs/105"); detail.Code != http.StatusNotFound {
		t.Fatalf("the run is still there: %d", detail.Code)
	}

	expectError(
		t, get(t, handler, "/api/v1/runs/105/data"),
		http.StatusNotFound, runs.ErrorReasonDataNotFound,
	)

	expectError(
		t, do(t, handler, http.MethodDelete, "/api/v1/runs/105", ""),
		http.StatusNotFound, runs.ErrorReasonRunNotFound,
	)
}

// TestRunDataEndpoint covers the one route that returns stored contents: it
// answers with the bytes as they were stored, as an opaque download.
func TestRunDataEndpoint(t *testing.T) {
	t.Parallel()

	handler, application := newTestRouter(t)

	newRun(t, application, 106, 1)
	newRun(t, application, 107, 1)

	payload := []byte("<script>alert(1)</script>")
	if err := application.Service.UpdateTestData(t.Context(), 106, payload); err != nil {
		t.Fatalf("failed to store the payload: %v", err)
	}

	rec := get(t, handler, "/api/v1/runs/106/data")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	if rec.Body.String() != string(payload) {
		t.Fatalf("expected the stored bytes, got %q", rec.Body.String())
	}

	headers := map[string]string{
		"Content-Type":           "application/octet-stream",
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "no-store",
	}

	for name, want := range headers {
		if got := rec.Header().Get(name); got != want {
			t.Fatalf("expected %s: %q, got %q", name, want, got)
		}
	}

	expectError(
		t, get(t, handler, "/api/v1/runs/107/data"),
		http.StatusNotFound, runs.ErrorReasonDataNotFound,
	)
}

// connIDPath renders a connection ID the way the route expects it.
func connIDPath(id runs.ConnID) string {
	return strconv.FormatUint(uint64(id), 10)
}
