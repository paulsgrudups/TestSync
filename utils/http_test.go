package utils

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
)

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
