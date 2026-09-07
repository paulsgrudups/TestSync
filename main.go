// Package main is the TestSync server entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/paulsgrudups/testsync/internal/app"
	"github.com/paulsgrudups/testsync/storage"
	"github.com/paulsgrudups/testsync/utils"

	"github.com/gorilla/websocket"

	"github.com/spf13/pflag"
)

// shutdownTimeout bounds the whole shutdown sequence. Shutdown used to be
// given [context.Background](), so one slow request could hold the process open
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

	logger, err := setupLogging(conf.Logging)
	if err != nil {
		return err
	}

	// From here on the operator has a log, so a failure is recorded there as
	// well as on the terminal they are watching.
	if err := serve(context.Background(), conf, logger); err != nil {
		logger.Error("server stopped", "error", err)

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

// setupLogging builds the process logger from the configured level, format and
// directory. A log directory that cannot be used is not fatal: the server
// falls back to stderr and says so on the logger it just built.
//
// The logger is also installed as the slog default, so the leaf packages that
// log without carrying one — wsutil's writer, the sqlite store — obey the
// operator's level and format too.
func setupLogging(conf utils.LogConfig) (*slog.Logger, error) {
	output, reason := logOutput(conf)

	logger, err := utils.NewLogger(conf, output)
	if err != nil {
		return nil, err
	}

	slog.SetDefault(logger)

	if reason != "" {
		logger.Warn("logging to stderr", "reason", reason)
	}

	return logger, nil
}

// logOutput opens the log file, falling back to stderr with the reason it
// could not. The reason is returned rather than logged because there is no
// logger yet at the point it is discovered.
func logOutput(conf utils.LogConfig) (io.Writer, string) {
	logDir := conf.Dir
	if strings.TrimSpace(logDir) == "" {
		logDir = "."
	}

	if err := os.MkdirAll(logDir, 0750); err != nil {
		return os.Stderr, "could not create the log directory: " + err.Error()
	}

	file, err := os.OpenFile(
		path.Join(logDir, "test-sync.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600,
	)
	if err != nil {
		return os.Stderr, "could not open the log file: " + err.Error()
	}

	return file, ""
}

// serve builds the process and runs it until a signal arrives or a server
// fails to listen.
func serve(ctx context.Context, conf utils.Config, logger *slog.Logger) error {
	validator, err := setupAuth(conf, *insecureNoAuth, logger)
	if err != nil {
		return authError(err)
	}

	if t := strings.ToLower(conf.Storage.Type); t != "" && t != utils.StorageTypeSQLite {
		logger.WarnContext(ctx, "storage type is no longer supported; using sqlite instead",
			"configured", conf.Storage.Type,
		)
	}

	store, err := storage.NewSQLiteStore(ctx, conf.Storage.SQLitePath, logger)
	if err != nil {
		return fmt.Errorf(
			"could not open the sqlite database at %q: %w", conf.Storage.SQLitePath, err,
		)
	}

	logger.InfoContext(ctx, "using sqlite data store", "path", store.Path())

	// One explicit construction point. Nothing below reads process-wide state,
	// so the order of these lines cannot change how the server behaves
	// (CODE-1).
	application := app.New(conf, store, validator, logger)

	handler, err := api.NewRouter(application)
	if err != nil {
		return fmt.Errorf("could not build the HTTP routes: %w", err)
	}

	// The janitor is owned here, not by route registration, so it can be
	// configured and stopped (STAB-5).
	janitor := runs.NewJanitor(
		conf.Cleanup.Interval.Duration(), conf.Cleanup.Retention.Duration(),
		application.Registry, application.Service, logger,
	)

	janitorCtx, stopJanitor := context.WithCancel(context.Background())
	defer stopJanitor()

	janitor.Start(janitorCtx)

	wsServer := ws.StartWebSocketServer(application)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", conf.HTTPPort),
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  10 * time.Second,
	}

	httpErr := listen(server, conf.HTTPPort, logger)

	logger.InfoContext(ctx, "testsync started",
		"http_port", conf.HTTPPort, "ws_port", conf.WSPort,
	)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	var listenErr error

	select {
	case <-stop:
		logger.InfoContext(ctx, "signal received, shutting down")
	case listenErr = <-httpErr:
	case listenErr = <-wsServer.ListenErr():
	}

	if err := shutdown(application, server, wsServer, janitor); err != nil && listenErr == nil {
		listenErr = err
	}

	logger.InfoContext(ctx, "testsync stopped")

	return listenErr
}

// listen starts accepting HTTP requests and reports a fatal listen error, such
// as a port already in use, on the returned channel. The error used to be
// raised as a panic inside the accept goroutine, where it arrived as a stack
// trace attributed to nothing in particular (STAB-7).
func listen(server *http.Server, port int, logger *slog.Logger) <-chan error {
	failed := make(chan error, 1)

	go func() {
		defer utils.RecoverGoroutine(logger, "http listener")

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
	a *app.App, server *http.Server, wsServer *ws.Server, janitor *runs.Janitor,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	var failure error

	// Stop accepting upgrades first: hijacked connections are not tracked by
	// http.Server, so this returns as soon as the listener is closed.
	if err := wsServer.Shutdown(ctx); err != nil {
		a.Log.Error("failed to stop the websocket server", "error", err)

		failure = fmt.Errorf("websocket server shutdown: %w", err)
	}

	// Drain the HTTP server while the store is still open.
	if err := server.Shutdown(ctx); err != nil {
		a.Log.Error("failed to stop the http server", "error", err)

		if failure == nil {
			failure = fmt.Errorf("http server shutdown: %w", err)
		}
	}

	closed := a.Registry.CloseAllConnections(
		ctx, websocket.CloseServiceRestart, "server shutting down",
	)
	if closed > 0 {
		a.Log.Info("closed agent connections with code 1012 (service restart)",
			"connections", closed,
		)
	}

	janitor.Stop()

	if err := a.Store.Close(); err != nil {
		a.Log.Error("failed to close the data store", "error", err)

		if failure == nil {
			failure = fmt.Errorf("closing the data store: %w", err)
		}
	}

	return failure
}

// setupAuth builds the one credential validator both the HTTP and the
// WebSocket server authenticate through. Authentication is required unless it
// is explicitly disabled, in which case every startup says so loudly (SEC-1).
func setupAuth(
	conf utils.Config, insecure bool, logger *slog.Logger,
) (*auth.Validator, error) {
	authConf := conf.Auth
	if insecure {
		authConf.Mode = utils.AuthModeNone
	}

	validator, err := auth.NewFromConfig(authConf, conf.SyncClient)
	if err != nil {
		return nil, err
	}

	if validator.Disabled() {
		for _, line := range []string{
			"****************************************************************",
			`** AUTHENTICATION IS DISABLED (auth mode "none").`,
			"** Anyone who can reach these ports may read, overwrite and",
			"** release the data of every test run.",
			"** Use this only on a trusted development machine.",
			"****************************************************************",
		} {
			logger.Warn(line)
		}
	}

	return validator, nil
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
