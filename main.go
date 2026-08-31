// Package main is the TestSync server entrypoint.
package main

import (
	"context"
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

	log "github.com/sirupsen/logrus"

	"github.com/spf13/pflag"
)

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
	pflag.Parse()

	if *help {
		pflag.PrintDefaults()
		return
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	var conf utils.Config

	err := utils.ReadConfig(
		filepath.Join(*configDir, "configuration.json"), &conf,
	)
	if err != nil {
		panic(err)
	}

	utils.ApplyDefaults(&conf)

	level, err := log.ParseLevel(conf.Logging.Level)
	if err != nil {
		panic(err)
	}

	log.SetLevel(level)

	logDir := conf.Logging.Dir
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

	if err := setupAuth(conf, *insecureNoAuth); err != nil {
		fatalAuth(err)
	}

	if t := strings.ToLower(conf.Storage.Type); t != "" && t != utils.StorageTypeSQLite {
		log.Warnf(
			"Storage type %q is no longer supported; using sqlite instead",
			conf.Storage.Type,
		)
	}

	store, err := storage.NewSQLiteStore(conf.Storage.SQLitePath)
	if err != nil {
		panic(err)
	}

	log.Infof("Using sqlite data store at %q", store.Path())

	runs.SetDataStore(store)

	wsServer := ws.StartWebSocketServer(conf.WSPort)

	handler, err := api.HandleRoutes()
	if err != nil {
		panic(err)
	}

	log.Info("Welcome to Test Sync")

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", conf.HTTPPort),
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  10 * time.Second,
	}

	go func() {
		err := server.ListenAndServe()
		if err == http.ErrServerClosed {
			return
		}

		if err != nil {
			panic(err)
		}
	}()

	<-stop

	if err := store.Close(); err != nil {
		log.Errorf("Failed to close data store: %s", err.Error())
	}

	server.Shutdown(context.Background())   //nolint:gosec,errcheck // graceful shutdown; errors are non-fatal here
	wsServer.Shutdown(context.Background()) //nolint:gosec,errcheck // graceful shutdown; errors are non-fatal here

	log.Info("GOODBYE")
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

// fatalAuth reports an unusable authentication configuration and stops the
// process. The message goes to stderr as well as the log, because the log is
// usually a file the operator is not watching while starting the server.
func fatalAuth(err error) {
	msg := fmt.Sprintf(`Refusing to start: %s

TestSync requires authentication. Configure credentials in configuration.json:

  "sync_client": {"username": "...", "password": "..."}

To run without authentication (development machines only), opt out explicitly:

  "auth": {"mode": "none"}

or start the server with --insecure-no-auth.`, err)

	log.Error(msg)
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
