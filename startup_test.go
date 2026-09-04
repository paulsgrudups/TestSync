package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/api/ws"
	"github.com/paulsgrudups/testsync/internal/app"
	"github.com/paulsgrudups/testsync/internal/storagetest"
	"github.com/paulsgrudups/testsync/utils"
)

// writeConfig writes a configuration file and returns its directory.
func writeConfig(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()

	err := os.WriteFile(
		filepath.Join(dir, "configuration.json"), []byte(body), 0o600,
	)
	if err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	return dir
}

// TestStartupErrorsAreReadable covers STAB-7: an operator mistake is a
// sentence they can act on, not a runtime panic with a stack trace. main turns
// each of these into one line on stderr and exit code 1.
func TestStartupErrorsAreReadable(t *testing.T) {
	valid := `{"http_port":19104,"ws_port":19105,` +
		`"sync_client":{"username":"u","password":"p"}}`

	cases := []struct {
		name    string
		dir     string
		body    string
		missing bool
		wants   []string
	}{
		{
			name:    "missing config file",
			missing: true,
			wants:   []string{"no configuration file at", "configuration.json", "-c"},
		},
		{
			name:  "unparseable config file",
			body:  `{"http_port": `,
			wants: []string{"could not read", "configuration.json"},
		},
		{
			name:  "invalid log level",
			body:  `{"logging":{"level":"VERBOSE"},` + strings.TrimPrefix(valid, "{"),
			wants: []string{"invalid logging.level", "VERBOSE", "DEBUG, INFO, WARN or ERROR"},
		},
		{
			name:  "invalid port",
			body:  `{"http_port":70000,"ws_port":19105}`,
			wants: []string{"invalid configuration", "http_port is 70000", "between 1 and 65535"},
		},
		{
			name:  "colliding ports",
			body:  `{"http_port":19104,"ws_port":19104}`,
			wants: []string{"both 19104", "different ports"},
		},
		{
			name:  "negative limit",
			body:  `{"limits":{"max_tests":-1}}`,
			wants: []string{"limits.max_tests is -1", "must be positive"},
		},
		{
			name:  "unparseable retention",
			body:  `{"cleanup":{"retention":"soon"}}`,
			wants: []string{"could not read", `invalid duration "soon"`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if !tc.missing {
				dir = writeConfig(t, tc.body)
			}

			conf, err := loadConfig(dir)
			if err == nil {
				err = setupLogging(conf.Logging)
			}

			if err == nil {
				t.Fatal("expected a startup error, got none")
			}

			message := err.Error()

			if strings.Contains(message, "goroutine") || strings.Contains(message, ".go:") {
				t.Fatalf("startup error reads like a stack trace: %s", message)
			}

			for _, want := range tc.wants {
				if !strings.Contains(message, want) {
					t.Fatalf("expected the error to mention %q, got: %s", want, message)
				}
			}
		})
	}
}

// TestListenReportsBindFailure covers the other half of STAB-7: a port that is
// already in use used to panic from inside the accept goroutine, where the
// message could not even be attributed to the server that failed.
func TestListenReportsBindFailure(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to hold a port: %v", err)
	}
	defer func() { _ = held.Close() }()

	port := held.Addr().(*net.TCPAddr).Port

	server := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		ReadHeaderTimeout: time.Second,
	}

	select {
	case err := <-listen(server, port):
		if err == nil {
			t.Fatal("expected a listen error")
		}

		if !strings.Contains(err.Error(), "address already in use") {
			t.Fatalf("expected an address-in-use error, got: %v", err)
		}

		if !strings.Contains(err.Error(), fmt.Sprintf("port %d", port)) {
			t.Fatalf("expected the error to name the port, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a fatal listen error was not reported")
	}
}

// TestConfigDefaultsAreUsable covers the happy path of the same code: a config
// with only credentials in it starts with sane ports, retention and limits.
func TestConfigDefaultsAreUsable(t *testing.T) {
	dir := writeConfig(t, `{"sync_client":{"username":"u","password":"p"}}`)

	conf, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if conf.HTTPPort != utils.DefaultHTTPPort || conf.WSPort != utils.DefaultWSPort {
		t.Fatalf("unexpected ports: %d, %d", conf.HTTPPort, conf.WSPort)
	}

	if conf.Cleanup.Retention.Duration() != utils.DefaultRetention {
		t.Fatalf("unexpected retention: %s", conf.Cleanup.Retention.Duration())
	}

	if runs.LimitsFromConfig(conf.Limits) != runs.DefaultLimits() {
		t.Fatalf("unexpected limits: %+v", conf.Limits)
	}
}

// TestShutdownLetsInFlightRequestsFinish is the STAB-6 regression test.
//
// Shutdown closed the data store first and only then asked the servers to
// stop, so a request that was already running read from a closed database and
// failed with "sql: database is closed" — on every deploy, and looking exactly
// like a flaky test to whoever hit it.
func TestShutdownLetsInFlightRequestsFinish(t *testing.T) {
	conf := utils.Config{}
	utils.ApplyDefaults(&conf)

	store := storagetest.NewStore(t)
	application := app.New(conf, store, auth.NewDisabledValidator())

	if err := store.SaveData(7, []byte("payload")); err != nil {
		t.Fatalf("failed to seed data: %v", err)
	}

	started := make(chan struct{})
	storeErr := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/tests/7", func(w http.ResponseWriter, _ *http.Request) {
		close(started)

		// Stands in for any request that is still working when the signal
		// arrives: it touches the store after shutdown has begun.
		time.Sleep(300 * time.Millisecond)

		data, _, err := store.LoadData(7)
		storeErr <- err

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		_, _ = w.Write(data)
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()

	status := make(chan int, 1)

	go func() {
		req, err := http.NewRequestWithContext(
			context.Background(), http.MethodGet,
			"http://"+listener.Addr().String()+"/tests/7", nil,
		)
		if err != nil {
			status <- -1
			return
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			status <- -1
			return
		}
		defer func() { _ = resp.Body.Close() }()

		status <- resp.StatusCode
	}()

	<-started

	janitor := runs.NewJanitor(
		time.Hour, time.Hour, application.Registry, application.Service,
	)
	janitor.Start(t.Context())

	begun := time.Now()

	if err := shutdown(application, server, &ws.Server{}, janitor); err != nil {
		t.Fatalf("shutdown reported: %v", err)
	}

	if elapsed := time.Since(begun); elapsed > shutdownTimeout {
		t.Fatalf("shutdown took %s, longer than the %s budget", elapsed, shutdownTimeout)
	}

	if err := <-storeErr; err != nil {
		t.Fatalf("an in-flight request hit a closed store: %v", err)
	}

	if code := <-status; code != http.StatusOK {
		t.Fatalf("an in-flight request did not complete: status %d", code)
	}

	// The store is closed once nothing can read from it any more, and the
	// janitor is stopped rather than left sweeping a closed database.
	if _, _, err := store.LoadData(7); err == nil {
		t.Fatal("the data store was left open after shutdown")
	}
}
