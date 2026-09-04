package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthEndpoint is the TEST-2 regression test. It used to pass only
// because Go runs test files in source order: api_test.go sorts first and
// installed the credential global this test never set, so renaming either file
// changed whether this one passed.
func TestHealthEndpoint(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}
