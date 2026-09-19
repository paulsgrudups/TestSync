package utils

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHTTPErrorOmitsTheReason pins the promise that came with the reason
// field: a response written without one is byte-identical to what it has
// always been, so no existing client sees a new key appear.
func TestHTTPErrorOmitsTheReason(t *testing.T) {
	rr := httptest.NewRecorder()

	HTTPError(rr, "bad request", http.StatusBadRequest)

	const want = `{"code":400,"error":"bad request"}` + "\n"

	if got := rr.Body.String(); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

// TestHTTPErrorReasonCarriesTheReason covers the other half: a reason reaches
// the client as a field of its own, so a UI can branch on it rather than on
// the prose beside it.
func TestHTTPErrorReasonCarriesTheReason(t *testing.T) {
	rr := httptest.NewRecorder()

	HTTPErrorReason(rr, "Could not find test run", http.StatusNotFound, "run_not_found")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}

	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Code != http.StatusNotFound || resp.Reason != "run_not_found" {
		t.Fatalf("unexpected error response: %+v", resp)
	}
}

func TestHTTPError_ResponseShape(t *testing.T) {
	rr := httptest.NewRecorder()

	HTTPError(rr, "bad request", http.StatusBadRequest)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}

	if contentType := rr.Header().Get("Content-Type"); contentType == "" {
		t.Fatal("expected Content-Type header to be set")
	}

	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Code != http.StatusBadRequest || resp.Error != "bad request" {
		t.Fatalf("unexpected error response: %+v", resp)
	}
}

// TestRoutePatternDropsVariablePatterns covers the route label: a variable's
// pattern is dropped, including one with braces of its own, so labels read as
// the route and a regex change is not a new series.
func TestRoutePatternDropsVariablePatterns(t *testing.T) {
	t.Parallel()

	for template, want := range map[string]string{
		`/tests/{testID:\d+}`:                         `/tests/{testID}`,
		`/register/{testID:[0-9]{1,19}}`:              `/register/{testID}`,
		`/runs/{testID:\d+}/connections/{connID:\d+}`: `/runs/{testID}/connections/{connID}`,
		`/health`:       `/health`,
		`/plain/{name}`: `/plain/{name}`,
	} {
		if got := routePattern.ReplaceAllString(template, "{$1}"); got != want {
			t.Errorf("%s: got %s, want %s", template, got, want)
		}
	}
}
