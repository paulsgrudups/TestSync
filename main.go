// Package main is the TestSync server entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/paulsgrudups/testsync/api"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/api/ws"
	"github.com/paulsgrudups/testsync/storage"
	"github.com/paulsgrudups/testsync/utils"

	"github.com/gorilla/websocket"

	log "github.com/sirupsen/logrus"

	"github.com/spf13/pflag"
)

// shutdownTimeout bounds the whole shutdown sequence. Shutdown used to be
// given context.Background(), so one slow request could hold the process open
// for as long as it liked and a restart would appear to hang (STAB-6).
const shutdownTimeout = 15 * time.Second

var (
	help      = pflag.BoolP("help", "h", false, "show help")
	configDir = pflag.StringP(
		"configDir", "c", "./config", "configuration file directory",
	)
	insecureNoAuth = pflag.Bool(
		"insecure-no-auth", false,
		"disable authentication entirely; development only",
	)
)

func main() {
	if err := run(); err != nil {
		// One readable line, no stack trace: every failure here is the
		// operator's to fix, not a bug to report (STAB-7).
		fmt.Fprintln(os.Stderr, "testsync: "+err.Error())
		os.Exit(1)
	}
}

// run starts the server and returns when it has stopped. Every failure is
// returned rather than panicked, so that a missing config file or an occupied
// port reads as a sentence instead of a runtime stack trace.
func run() error {
	pflag.Parse()

	if *help {
		pflag.PrintDefaults()
		return nil
	}

	conf, err := loadConfig(*configDir)
	if err != nil {
		return err
	}

	if err := setupLogging(conf.Logging); err != nil {
		return err
	}

	// From here on the operator has a log, so a failure is recorded there as
	// well as on the terminal they are watching.
	if err := serve(conf); err != nil {
		log.Error(err.Error())

		return err
	}

	return nil
}

// loadConfig reads and validates the configuration file.
func loadConfig(dir string) (utils.Config, error) {
	filename := filepath.Join(dir, "configuration.json")

	var conf utils.Config

	if err := utils.ReadConfig(filename, &conf); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return conf, fmt.Errorf(
				"no configuration file at %s: create it, or point -c at the directory holding it",
				filename,
			)
		}

		return conf, fmt.Errorf("could not read %s: %w", filename, err)
	}

	utils.ApplyDefaults(&conf)

	if err := utils.Validate(&conf); err != nil {
		return conf, fmt.Errorf("invalid configuration in %s: %w", filename, err)
	}

	return conf, nil
}

// setupLogging applies the configured level and output. A log directory that
// cannot be used is not fatal: the server falls back to stderr and says so.
func setupLogging(conf utils.LogConfig) error {
	level, err := log.ParseLevel(conf.Level)
	if err != nil {
		return fmt.Errorf(
			"invalid logging.level %q: use DEBUG, INFO, WARN or ERROR", conf.Level,
		)
	}

	log.SetLevel(level)

	logDir := conf.Dir
	if strings.TrimSpace(logDir) == "" {
		logDir = "."
	}

	if err := os.MkdirAll(logDir, 0750); err != nil {
		log.Infof("Failed to create log dir, using stderr: %s", err.Error())
		log.SetOutput(os.Stderr)
	} else {
		file, err := os.OpenFile(
			path.Join(logDir, "test-sync.log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666,
		)
		if err != nil {
			log.Info("Failed to log to file, using default stderr")
			log.SetOutput(os.Stderr)
		} else {
			log.SetOutput(file)
		}
	}

	log.SetFormatter(&log.TextFormatter{
		DisableLevelTruncation: true,
	})

	return nil
}

// serve builds the process and runs it until a signal arrives or a server
// fails to listen.
func serve(conf utils.Config) error {
	if err := setupAuth(conf, *insecureNoAuth); err != nil {
		return authError(err)
	}

	if t := strings.ToLower(conf.Storage.Type); t != "" && t != utils.StorageTypeSQLite {
		log.Warnf(
			"Storage type %q is no longer supported; using sqlite instead",
			conf.Storage.Type,
		)
	}

	store, err := storage.NewSQLiteStore(conf.Storage.SQLitePath)
	if err != nil {
		return fmt.Errorf(
			"could not open the sqlite database at %q: %w", conf.Storage.SQLitePath, err,
		)
	}

	log.Infof("Using sqlite data store at %q", store.Path())

	runs.SetDataStore(store)
	runs.SetLimits(runs.LimitsFromConfig(conf.Limits))

	handler, err := api.HandleRoutes()
	if err != nil {
		return fmt.Errorf("could not build the HTTP routes: %w", err)
	}

	// The janitor is owned here, not by route registration, so it can be
	// configured and stopped (STAB-5).
	janitor := runs.NewJanitor(
		conf.Cleanup.Interval.Duration(), conf.Cleanup.Retention.Duration(),
	)

	janitorCtx, stopJanitor := context.WithCancel(context.Background())
	defer stopJanitor()

	janitor.Start(janitorCtx)

	wsServer := ws.StartWebSocketServer(conf.WSPort)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", conf.HTTPPort),
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  10 * time.Second,
	}

	httpErr := listen(server, conf.HTTPPort)

	log.Info("Welcome to Test Sync")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	var listenErr error

	select {
	case <-stop:
		log.Info("Signal received, shutting down")
	case listenErr = <-httpErr:
	case listenErr = <-wsServer.ListenErr():
	}

	if err := shutdown(server, wsServer, janitor, store); err != nil && listenErr == nil {
		listenErr = err
	}

	log.Info("GOODBYE")

	return listenErr
}

// listen starts accepting HTTP requests and reports a fatal listen error, such
// as a port already in use, on the returned channel. The error used to be
// raised as a panic inside the accept goroutine, where it arrived as a stack
// trace attributed to nothing in particular (STAB-7).
func listen(server *http.Server, port int) <-chan error {
	failed := make(chan error, 1)

	go func() {
		defer utils.RecoverGoroutine("http listener")

		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- fmt.Errorf("http server on port %d: %w", port, err)
		}
	}()

	return failed
}

// shutdown stops the process in the only order that is safe for the clients
// that are still talking to it (STAB-6):
//
//  1. both servers stop accepting, so no new work arrives;
//  2. requests already in flight are allowed to finish;
//  3. WebSocket agents are told the server is restarting, with close code
//     1012, so that a deploy is distinguishable from a crash;
//  4. the janitor stops;
//  5. the data store closes last, once nothing can read from it any more.
//
// The whole sequence is bounded by shutdownTimeout. The store used to be
// closed first, so requests in flight during a restart failed with
// "sql: database is closed" and looked like flaky tests.
func shutdown(
	server *http.Server, wsServer *ws.Server, janitor *runs.Janitor, store *storage.SQLiteStore,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	var failure error

	// Stop accepting upgrades first: hijacked connections are not tracked by
	// http.Server, so this returns as soon as the listener is closed.
	if err := wsServer.Shutdown(ctx); err != nil {
		log.Errorf("Failed to stop the WebSocket server: %s", err.Error())

		failure = fmt.Errorf("websocket server shutdown: %w", err)
	}

	// Drain the HTTP server while the store is still open.
	if err := server.Shutdown(ctx); err != nil {
		log.Errorf("Failed to stop the HTTP server: %s", err.Error())

		if failure == nil {
			failure = fmt.Errorf("http server shutdown: %w", err)
		}
	}

	closed := runs.CloseAllConnections(
		ctx, websocket.CloseServiceRestart, "server shutting down",
	)
	if closed > 0 {
		log.Infof("Closed %d agent connections with code 1012 (service restart)", closed)
	}

	janitor.Stop()

	if err := store.Close(); err != nil {
		log.Errorf("Failed to close data store: %s", err.Error())

		if failure == nil {
			failure = fmt.Errorf("closing the data store: %w", err)
		}
	}

	return failure
}

// setupAuth resolves the process-wide credential validator and installs it for
// both the HTTP and the WebSocket server. Authentication is required unless it
// is explicitly disabled, in which case every startup says so loudly (SEC-1).
func setupAuth(conf utils.Config, insecure bool) error {
	authConf := conf.Auth
	if insecure {
		authConf.Mode = utils.AuthModeNone
	}

	validator, err := auth.NewFromConfig(authConf, conf.SyncClient)
	if err != nil {
		return err
	}

	if validator.Disabled() {
		log.Warn("****************************************************************")
		log.Warn("** AUTHENTICATION IS DISABLED (auth mode \"none\").")
		log.Warn("** Anyone who can reach these ports may read, overwrite and")
		log.Warn("** release the data of every test run.")
		log.Warn("** Use this only on a trusted development machine.")
		log.Warn("****************************************************************")
	}

	auth.SetShared(validator)

	return nil
}

// authError explains an unusable authentication configuration. The first line
// says what is wrong; the rest says what to do about it, because this is the
// failure an operator is most likely to meet on a first run.
func authError(err error) error {
	return fmt.Errorf(`refusing to start: %w

TestSync requires authentication. Configure credentials in configuration.json:

  "sync_client": {"username": "...", "password": "..."}

To run without authentication (development machines only), opt out explicitly:

  "auth": {"mode": "none"}

or start the server with --insecure-no-auth`, err)
}
