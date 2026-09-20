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

// loadConfig loads the file in dir with no environment and no flags. required
// is what an explicit -c means: the file must be there.
func loadConfig(t *testing.T, dir string, required bool) (utils.Config, error) {
	t.Helper()

	loaded, err := utils.Load(utils.LoadOptions{Dir: dir, RequireFile: required})

	return loaded.Config, err
}

// TestStartupErrorsAreReadable covers STAB-7: an operator mistake is a
// sentence they can act on, not a runtime panic with a stack trace. main turns
// each of these into one line on stderr and exit code 1.
func TestStartupErrorsAreReadable(t *testing.T) {
	valid := `{"http_port":19104,` +
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
			wants: []string{
				"no configuration file at", "configuration.json", "-c", "TESTSYNC_",
			},
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
			body:  `{"http_port":70000}`,
			wants: []string{"invalid configuration", "http_port is 70000", "between 1 and 65535"},
		},
		{
			name:  "invalid deprecated ws_port",
			body:  `{"http_port":19104,"ws_port":70000}`,
			wants: []string{"ws_port is 70000", "between 1 and 65535"},
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

			conf, err := loadConfig(t, dir, true)
			if err == nil {
				_, err = setupLogging(conf.Logging)
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
	case err := <-listen(server, utils.DiscardLogger()):
		if err == nil {
			t.Fatal("expected a listen error")
		}

		if !strings.Contains(err.Error(), "address already in use") {
			t.Fatalf("expected an address-in-use error, got: %v", err)
		}

		if !strings.Contains(err.Error(), fmt.Sprintf(":%d", port)) {
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

	conf, err := loadConfig(t, dir, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// One port; the deprecated second listener is off unless asked for.
	if conf.HTTPPort != utils.DefaultHTTPPort || conf.WSPort != 0 {
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
	application := app.New(conf, store, auth.NewDisabledValidator(), utils.DiscardLogger())

	if err := store.SaveData(t.Context(), 7, []byte("payload")); err != nil {
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

		data, _, err := store.LoadData(t.Context(), 7)
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
		time.Hour, time.Hour, application.Registry, application.Service, nil,
	)
	janitor.Start(t.Context())

	begun := time.Now()

	if err := shutdown(application, []*http.Server{server}, janitor); err != nil {
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
	if _, _, err := store.LoadData(t.Context(), 7); err == nil {
		t.Fatal("the data store was left open after shutdown")
	}
}

// TestStartsFromTheEnvironmentAlone is the API-5 and SEC-6 done-condition: with
// no configuration file anywhere, one environment variable holding the
// password is enough for a server that requires authentication.
func TestStartsFromTheEnvironmentAlone(t *testing.T) {
	env := map[string]string{"TESTSYNC_SYNC_CLIENT_PASSWORD": "s3cret"}

	loaded, err := utils.Load(utils.LoadOptions{
		Dir: t.TempDir(),
		LookupEnv: func(name string) (string, bool) {
			v, ok := env[name]
			return v, ok
		},
	})
	if err != nil {
		t.Fatalf("could not load a configuration from the environment: %v", err)
	}

	if loaded.File != "" {
		t.Fatalf("expected no configuration file, got %s", loaded.File)
	}

	validator, err := setupAuth(loaded.Config, false, utils.DiscardLogger())
	if err != nil {
		t.Fatalf("authentication could not be set up: %v", err)
	}

	if validator.Disabled() || !validator.Validate(utils.DefaultUsername, "s3cret") {
		t.Fatal("the environment's password is not the one the server checks")
	}
}

// TestStartupWithoutCredentialsSaysHowToProvideThem covers the other side: no
// file and no variables is refused, and the message names the variable.
func TestStartupWithoutCredentialsSaysHowToProvideThem(t *testing.T) {
	loaded, err := utils.Load(utils.LoadOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("a missing optional file must not be an error: %v", err)
	}

	_, err = setupAuth(loaded.Config, false, utils.DiscardLogger())
	if err == nil {
		t.Fatal("expected a server with no credentials to be refused")
	}

	// serve wraps the refusal in authError; that is what the operator reads.
	if !strings.Contains(authError(err).Error(), "TESTSYNC_SYNC_CLIENT_PASSWORD") {
		t.Fatalf("the error does not say how to provide a password: %v", err)
	}
}

// TestExampleConfigurationIsCurrent loads the shipped example the way the
// README tells a new user to: every key in it must be one the server knows,
// and every value must be the default, so the file cannot drift from the code
// it documents.
func TestExampleConfigurationIsCurrent(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("config", "configuration.example.json"))
	if err != nil {
		t.Fatalf("the example configuration is missing: %v", err)
	}

	loaded, err := utils.Load(utils.LoadOptions{
		Dir: writeConfig(t, string(body)), RequireFile: true,
	})
	if err != nil {
		t.Fatalf("the example does not load: %v", err)
	}

	for _, warning := range loaded.Warnings {
		if !strings.Contains(warning, "readable by every user") {
			t.Errorf("the example draws a warning: %s", warning)
		}
	}

	defaults := utils.Config{SyncClient: loaded.Config.SyncClient}
	utils.ApplyDefaults(&defaults)

	if loaded.Config != defaults {
		t.Fatalf("the example's values are not the defaults:\n example: %+v\ndefaults: %+v",
			loaded.Config, defaults)
	}

	// Every setting the server has appears in the example.
	for name, key := range utils.EnvVars() {
		if key == "sync_client.password_file" || key == "storage.type" || key == "ws_port" {
			continue // alternatives and deprecated keys, documented elsewhere
		}

		leaf := key[strings.LastIndex(key, ".")+1:]
		if !strings.Contains(string(body), `"`+leaf+`"`) {
			t.Errorf("the example is missing %s (%s)", key, name)
		}
	}
}
