package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/utils"
)

func TestCreateAndReadTestData(t *testing.T) {
	runs.SyncClient = utils.BasicCredentials{Username: "user", Password: "pass"}
	runs.AllTests = make(map[int]*runs.Test)

	handler, err := HandleRoutes()
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	postReq := httptest.NewRequest(http.MethodPost, "/tests/123", strings.NewReader("payload"))
	postReq.SetBasicAuth("user", "pass")
	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, postRec.Code)
	}

	if postRec.Body.String() != "payload" {
		t.Fatalf("unexpected body: %q", postRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/tests/123", nil)
	getReq.SetBasicAuth("user", "pass")
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, getRec.Code)
	}

	read, err := io.ReadAll(getRec.Body)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}
	if string(read) != "payload" {
		t.Fatalf("unexpected body: %q", string(read))
	}
}
