package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthEndpoint(t *testing.T) {
	handler, err := HandleRoutes()
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

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
