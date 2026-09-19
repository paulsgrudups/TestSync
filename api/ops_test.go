package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paulsgrudups/testsync/internal/app"
	"github.com/paulsgrudups/testsync/internal/apptest"
	"github.com/paulsgrudups/testsync/internal/buildinfo"
)

// newAppRouter builds a router and returns the application behind it, for
// tests that need to reach the store or the counters.
func newAppRouter(t *testing.T) (http.Handler, *app.App) {
	t.Helper()

	application := apptest.NewDefault(t)

	handler, err := NewRouter(application)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	return handler, application
}

// get performs an unauthenticated GET.
func get(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	return rec
}

// expectJSON asserts a status, a JSON content type and an exact body.
func expectJSON(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()

	if rec.Code != status {
		t.Fatalf("expected status %d, got %d (%s)", status, rec.Code, rec.Body.String())
	}

	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("unexpected Content-Type %q", got)
	}

	if rec.Body.String() != body {
		t.Fatalf("\n got: %s\nwant: %s", rec.Body.String(), body)
	}
}

// TestRootDescribesTheServer is the API-4 regression test: GET / used to
// answer with a placeholder proverb.
func TestRootDescribesTheServer(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(t)

	want, err := json.Marshal(buildinfo.Describe())
	if err != nil {
		t.Fatalf("failed to encode: %v", err)
	}

	expectJSON(t, get(t, handler, "/"), http.StatusOK, string(want))

	if !strings.Contains(string(want), `"service":"testsync"`) ||
		!strings.Contains(string(want), `"protocol":1`) {
		t.Fatalf("descriptor is missing its identity: %s", want)
	}
}

// TestVersionReportsTheBuild covers GET /version, which needs no credentials.
func TestVersionReportsTheBuild(t *testing.T) {
	t.Parallel()

	rec := get(t, newTestRouter(t), "/version")

	var info buildinfo.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("unexpected /version answer %d: %s", rec.Code, rec.Body.String())
	}

	if info.Version == "" || info.GoVersion == "" || info.Protocol != buildinfo.ProtocolVersion {
		t.Fatalf("incomplete build info: %+v", info)
	}
}

// TestLivenessProbes covers /healthz and its original name /health, which
// answer without credentials and without checking any dependency.
func TestLivenessProbes(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(t)

	for _, path := range []string{"/healthz", "/health"} {
		expectJSON(t, get(t, handler, path), http.StatusOK, `{"status":"ok"}`)
	}
}

// TestReadinessFollowsTheStore is the API-1 readiness test: /health used to
// answer ok even with the database gone, which made it useless for taking a
// broken instance out of rotation.
func TestReadinessFollowsTheStore(t *testing.T) {
	t.Parallel()

	handler, application := newAppRouter(t)

	expectJSON(t, get(t, handler, "/readyz"), http.StatusOK,
		`{"status":"ready","checks":{"storage":"ok"}}`)

	if err := application.Store.Close(); err != nil {
		t.Fatalf("failed to close the store: %v", err)
	}

	expectJSON(t, get(t, handler, "/readyz"), http.StatusServiceUnavailable,
		`{"status":"not_ready","checks":{"storage":"failing"}}`)

	// Liveness is unaffected: restarting the process would not bring the
	// database back.
	expectJSON(t, get(t, handler, "/healthz"), http.StatusOK, `{"status":"ok"}`)
}

// TestMetricsNeedCredentials covers /metrics sitting behind the same
// credentials as the runs whose activity it counts.
func TestMetricsNeedCredentials(t *testing.T) {
	t.Parallel()

	if rec := get(t, newTestRouter(t), "/metrics"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without credentials, got %d", rec.Code)
	}
}

// TestMetricsExposeTheServer covers the API-1 metrics: the gauges read from the
// registry, and the request counter labelled by route template rather than by
// path, so a test ID never becomes a label.
func TestMetricsExposeTheServer(t *testing.T) {
	t.Parallel()

	handler, application := newAppRouter(t)

	if _, err := application.Registry.Ensure(42); err != nil {
		t.Fatalf("failed to register a run: %v", err)
	}

	if rec := request(t, handler, http.MethodPost, "/tests/42", []byte("x")); rec.Code != http.StatusCreated {
		t.Fatalf("failed to store: %d", rec.Code)
	}

	rec := request(t, handler, http.MethodGet, "/metrics", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if got := rec.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("unexpected Content-Type %q", got)
	}

	body := rec.Body.String()

	for _, line := range []string{
		"# TYPE testsync_build_info gauge",
		"testsync_runs_active 1",
		"testsync_connections_active 0",
		"testsync_checkpoints_waiting 0",
		"# TYPE testsync_checkpoint_releases_total counter",
		`testsync_requests_total{route="/tests/{testID}",code="201"} 1`,
		"# TYPE testsync_websocket_commands_total counter",
	} {
		if !strings.Contains(body, line+"\n") {
			t.Errorf("metrics are missing %q", line)
		}
	}

	if strings.Contains(body, "/tests/42") {
		t.Fatalf("a test ID leaked into a label:\n%s", body)
	}
}
