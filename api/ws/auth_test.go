package ws

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/internal/apptest"
	"github.com/paulsgrudups/testsync/utils"
)

// authTestID is the run the authentication tests register against.
const authTestID = 4242

// newAuthTestServer starts a WebSocket server that authenticates through the
// given validator. It is the same validator type the HTTP server holds, so the
// two paths cannot disagree about who is allowed in (SEC-1).
func newAuthTestServer(t *testing.T, validator *auth.Validator) *httptest.Server {
	t.Helper()

	application := apptest.WithValidator(t, apptest.Config(), validator)

	httpServer := httptest.NewServer(newWSRouter(newServer(application)))
	t.Cleanup(httpServer.Close)

	return httpServer
}

// dialAuth dials the register route and reports the resulting status code. A
// successful upgrade reports [http.StatusSwitchingProtocols].
func dialAuth(t *testing.T, server *httptest.Server, query string, header http.Header) int {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/register/" +
		strconv.Itoa(authTestID) + query

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	if err == nil {
		defer func() { _ = conn.Close() }()

		return http.StatusSwitchingProtocols
	}

	if resp == nil {
		t.Fatalf("failed to dial %s with no response: %v", wsURL, err)
	}

	return resp.StatusCode
}

// TestWebSocketAuthorization covers SEC-1 and SEC-2 on the WebSocket path: the
// same validator as the HTTP path decides, wrong credentials are rejected, and
// the deprecated query-parameter fallback keeps working.
func TestWebSocketAuthorization(t *testing.T) {
	t.Parallel()

	validator, err := auth.NewValidator(
		utils.BasicCredentials{Username: "user", Password: "pass"},
	)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	basicAuthHeader := func(user, pass string) http.Header {
		header := http.Header{}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.SetBasicAuth(user, pass)
		header.Set("Authorization", req.Header.Get("Authorization"))

		return header
	}

	queryCreds := func(user, pass string) string {
		values := url.Values{}
		values.Set("username", user)
		values.Set("password", pass)

		return "?" + values.Encode()
	}

	cases := []struct {
		name     string
		query    string
		header   http.Header
		expected int
	}{
		{
			name:     "correct credentials",
			header:   basicAuthHeader("user", "pass"),
			expected: http.StatusSwitchingProtocols,
		},
		{
			name:     "no credentials",
			expected: http.StatusUnauthorized,
		},
		{
			name:     "wrong username",
			header:   basicAuthHeader("nope", "pass"),
			expected: http.StatusUnauthorized,
		},
		{
			name:     "wrong password",
			header:   basicAuthHeader("user", "nope"),
			expected: http.StatusUnauthorized,
		},
		{
			name:     "query parameter fallback",
			query:    queryCreds("user", "pass"),
			expected: http.StatusSwitchingProtocols,
		},
		{
			name:     "query parameter fallback with wrong password",
			query:    queryCreds("user", "nope"),
			expected: http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newAuthTestServer(t, validator)

			if got := dialAuth(t, server, tc.query, tc.header); got != tc.expected {
				t.Fatalf("expected status %d, got %d", tc.expected, got)
			}
		})
	}
}

// TestWebSocketAuthorizationWithoutValidator covers the fail-closed default: a
// server whose validator was never installed must reject everyone rather than
// serve everyone, which is what the old empty-credential branch did (SEC-1).
func TestWebSocketAuthorizationWithoutValidator(t *testing.T) {
	t.Parallel()

	server := newAuthTestServer(t, nil)

	if got := dialAuth(t, server, "", nil); got != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, got)
	}
}

// TestWebSocketAuthorizationDisabled covers the explicit opt-out: with auth
// mode "none" a credential-less client still connects.
func TestWebSocketAuthorizationDisabled(t *testing.T) {
	t.Parallel()

	server := newAuthTestServer(t, auth.NewDisabledValidator())

	if got := dialAuth(t, server, "", nil); got != http.StatusSwitchingProtocols {
		t.Fatalf("expected status %d, got %d", http.StatusSwitchingProtocols, got)
	}
}
