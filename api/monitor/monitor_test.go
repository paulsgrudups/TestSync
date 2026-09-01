package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/utils"
	"github.com/paulsgrudups/testsync/wsutil"
)

// newTestRouter builds a router carrying only the monitoring routes, with the
// shared validator pointed at a known credential (SEC-1).
func newTestRouter(t *testing.T) http.Handler {
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

	previousTests := runs.AllTests
	t.Cleanup(func() { runs.AllTests = previousTests })
	runs.AllTests = make(map[int]*runs.Test)

	router := mux.NewRouter().StrictSlash(false)
	RegisterRoutes(router)

	return router
}

// get performs an authenticated GET against the monitoring routes.
func get(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetBasicAuth("user", "pass")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

// decode reads a JSON response body into out.
func decode(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("failed to decode response %q: %v", rec.Body.String(), err)
	}
}

// TestMonitorRoutesRequireCredentials covers SEC-1 for the new surface: the
// API and the page itself are unreachable without credentials, and the 401
// carries a challenge so that a browser can ask for them.
func TestMonitorRoutesRequireCredentials(t *testing.T) {
	handler := newTestRouter(t)

	paths := []string{
		"/api/v1/runs",
		"/api/v1/runs/123",
		"/ui",
		"/ui/",
		"/ui/app.js",
		"/ui/app.css",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
			}

			if challenge := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(challenge, "Basic ") {
				t.Fatalf("expected a Basic challenge, got %q", challenge)
			}
		})
	}
}

// TestMonitorRoutesRejectBadCredentials makes sure the monitoring routes go
// through the same validator as everything else rather than a second path.
func TestMonitorRoutesRejectBadCredentials(t *testing.T) {
	handler := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
	req.SetBasicAuth("user", "wrong")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

// TestListRunsEmpty covers the state an operator sees most often on a quiet
// server: no runs at all. The list must still be an array, never null.
func TestListRunsEmpty(t *testing.T) {
	handler := newTestRouter(t)

	rec := get(t, handler, "/api/v1/runs")

	var body struct {
		ServerTime time.Time        `json:"server_time"`
		RunCount   int              `json:"run_count"`
		Runs       *[]runSummaryDTO `json:"runs"`
	}

	decode(t, rec, &body)

	if body.RunCount != 0 {
		t.Fatalf("expected no runs, got %d", body.RunCount)
	}

	if body.Runs == nil {
		t.Fatal("expected an empty runs array, got null")
	}

	if len(*body.Runs) != 0 {
		t.Fatalf("expected an empty runs array, got %d entries", len(*body.Runs))
	}

	if body.ServerTime.IsZero() {
		t.Fatal("expected a server time")
	}

	if !strings.Contains(rec.Body.String(), `"runs":[]`) {
		t.Fatalf("expected an empty JSON array, got %q", rec.Body.String())
	}
}

// TestListRunsShape drives the shape an operator's page depends on: a run that
// is stuck on a barrier reports as waiting, a released one does not.
func TestListRunsShape(t *testing.T) {
	handler := newTestRouter(t)

	waiting, waitingIDs := newRun(t, 4242, 3)
	waiting.JoinCheckpoint("login-barrier", 3, runs.DefaultCheckpointTimeout, waitingIDs[0])
	waiting.JoinCheckpoint("login-barrier", 3, runs.DefaultCheckpointTimeout, waitingIDs[1])

	released, releasedIDs := newRun(t, 17, 1)
	released.JoinCheckpoint("warmup", 1, runs.DefaultCheckpointTimeout, releasedIDs[0])

	rec := get(t, handler, "/api/v1/runs")

	var body struct {
		RunCount int             `json:"run_count"`
		Runs     []runSummaryDTO `json:"runs"`
	}

	decode(t, rec, &body)

	if body.RunCount != 2 || len(body.Runs) != 2 {
		t.Fatalf("expected 2 runs, got %d (%s)", body.RunCount, rec.Body.String())
	}

	// Runs are ordered by test ID, so 17 comes before 4242.
	if body.Runs[0].TestID != 17 || body.Runs[1].TestID != 4242 {
		t.Fatalf("expected runs ordered by ID, got %d and %d", body.Runs[0].TestID, body.Runs[1].TestID)
	}

	if body.Runs[0].Waiting || body.Runs[0].WaitingCheckpoints != 0 {
		t.Fatalf("released run reported as waiting: %+v", body.Runs[0])
	}

	stuck := body.Runs[1]
	if !stuck.Waiting || stuck.WaitingCheckpoints != 1 || stuck.CheckpointCount != 1 {
		t.Fatalf("expected one waiting checkpoint, got %+v", stuck)
	}

	if stuck.ConnectionCount != 3 || stuck.ActiveConnectionCount != 3 {
		t.Fatalf("expected 3 active connections, got %+v", stuck)
	}

	if stuck.Created.IsZero() || stuck.AgeSeconds < 0 {
		t.Fatalf("expected a sane created time and age, got %+v", stuck)
	}
}

// TestRunDetailShape checks the per-run view, including the counts the page
// renders the progress bar from.
func TestRunDetailShape(t *testing.T) {
	handler := newTestRouter(t)

	run, ids := newRun(t, 900, 3)
	run.SetData([]byte("secret-payload"))
	run.JoinCheckpoint("stage-2", 3, runs.DefaultCheckpointTimeout, ids[0])
	run.JoinCheckpoint("stage-2", 3, runs.DefaultCheckpointTimeout, ids[2])
	run.JoinCheckpoint("stage-1", 1, runs.DefaultCheckpointTimeout, ids[0])
	run.GetConnection(ids[1]).Close()

	rec := get(t, handler, "/api/v1/runs/900")

	var body struct {
		Run         runSummaryDTO `json:"run"`
		Connections []struct {
			Index  int  `json:"index"`
			Active bool `json:"active"`
		} `json:"connections"`
		Checkpoints []struct {
			Identifier  string `json:"identifier"`
			TargetCount int    `json:"target_count"`
			JoinedCount int    `json:"joined_count"`
			RoundsDone  int    `json:"rounds_completed"`
			Waiting     bool   `json:"waiting"`
			Members     []int  `json:"members"`
		} `json:"checkpoints"`
	}

	decode(t, rec, &body)

	if body.Run.TestID != 900 || !body.Run.Waiting {
		t.Fatalf("unexpected run summary: %+v", body.Run)
	}

	if body.Run.DataSizeBytes != len("secret-payload") || !body.Run.HasData {
		t.Fatalf("expected the data size to be reported, got %+v", body.Run)
	}

	// The payload itself must never leave the server through this API.
	if strings.Contains(rec.Body.String(), "secret-payload") {
		t.Fatalf("stored test data leaked into the monitoring response: %s", rec.Body.String())
	}

	if len(body.Connections) != 3 {
		t.Fatalf("expected 3 connections, got %d", len(body.Connections))
	}

	if body.Connections[1].Active {
		t.Fatalf("expected connection #1 to be reported as closed: %+v", body.Connections)
	}

	if body.Run.ActiveConnectionCount != 2 {
		t.Fatalf("expected 2 active connections, got %d", body.Run.ActiveConnectionCount)
	}

	if len(body.Checkpoints) != 2 {
		t.Fatalf("expected 2 checkpoints, got %d", len(body.Checkpoints))
	}

	// Checkpoints are ordered by identifier.
	first := body.Checkpoints[0]
	if first.Identifier != "stage-1" || first.Waiting || first.RoundsDone != 1 {
		t.Fatalf("expected stage-1 to have completed one round and be idle, got %+v", first)
	}

	second := body.Checkpoints[1]
	if second.Identifier != "stage-2" || !second.Waiting || second.RoundsDone != 0 {
		t.Fatalf("expected stage-2 to be waiting in its first round, got %+v", second)
	}

	if second.TargetCount != 3 || second.JoinedCount != 2 {
		t.Fatalf("expected 2 of 3 agents joined, got %+v", second)
	}

	if len(second.Members) != 2 || second.Members[0] != 0 || second.Members[1] != 2 {
		t.Fatalf("expected members [0 2], got %+v", second.Members)
	}
}

// TestRunDetailNotFound covers a run ID that the server has never seen, which
// is what a stale bookmark looks like.
func TestRunDetailNotFound(t *testing.T) {
	handler := newTestRouter(t)

	rec := get(t, handler, "/api/v1/runs/321")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

// TestMonitorRoutesAreReadOnly makes sure no write verb was registered by
// accident: the monitor may never mutate a run.
func TestMonitorRoutesAreReadOnly(t *testing.T) {
	handler := newTestRouter(t)

	newRun(t, 55, 1)

	methods := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/v1/runs/55", strings.NewReader("{}"))
			req.SetBasicAuth("user", "pass")

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
				t.Fatalf("expected %s to be rejected, got %d", method, rec.Code)
			}
		})
	}
}

// TestUIPageIsServed covers the embedded page: it is served from the binary,
// references only same-origin assets and carries a restrictive policy.
func TestUIPageIsServed(t *testing.T) {
	handler := newTestRouter(t)

	rec := get(t, handler, "/ui")

	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("unexpected content type %q", contentType)
	}

	page := rec.Body.String()
	for _, want := range []string{"<title>TestSync monitor</title>", "/ui/app.js", "/ui/app.css"} {
		if !strings.Contains(page, want) {
			t.Fatalf("expected the page to contain %q", want)
		}
	}

	if policy := rec.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "default-src 'none'") {
		t.Fatalf("expected a restrictive policy, got %q", policy)
	}

	script := get(t, handler, "/ui/app.js")
	if contentType := script.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/javascript") {
		t.Fatalf("unexpected script content type %q", contentType)
	}

	if strings.Contains(script.Body.String(), "innerHTML") {
		t.Fatal("the page must not build nodes from server-derived strings with innerHTML")
	}
}

// runSummaryDTO mirrors the wire shape of a run summary, so a change to the
// JSON contract fails here rather than in an operator's browser.
type runSummaryDTO struct {
	TestID                int       `json:"test_id"`
	Created               time.Time `json:"created"`
	AgeSeconds            float64   `json:"age_seconds"`
	ConnectionCount       int       `json:"connection_count"`
	ActiveConnectionCount int       `json:"active_connection_count"`
	CheckpointCount       int       `json:"checkpoint_count"`
	WaitingCheckpoints    int       `json:"waiting_checkpoint_count"`
	Waiting               bool      `json:"waiting"`
	HasData               bool      `json:"has_data"`
	DataSizeBytes         int       `json:"data_size_bytes"`
	ForceEnd              bool      `json:"force_end"`
}

// newRun registers a run with the requested number of attached connections.
// The connections are never written to, so they need no live socket.
func newRun(t *testing.T, testID, connections int) (*runs.Test, []runs.ConnID) {
	t.Helper()

	run := runs.EnsureTest(testID, func() *runs.Test {
		return &runs.Test{Created: time.Now().UTC()}
	})

	ids := make([]runs.ConnID, 0, connections)
	for range connections {
		ids = append(ids, run.AddConnection(wsutil.NewClient(nil)))
	}

	return run, ids
}
