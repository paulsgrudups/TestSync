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

	if err := os.MkdirAll(logDir, 0755); err != nil {
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

	storageType := strings.ToLower(conf.Storage.Type)
	switch storageType {
	case "", "memory":
		runs.SetDataStore(storage.NewMemoryStore())
	case "sqlite":
		sqlitePath := conf.Storage.SQLitePath
		if sqlitePath == "" {
			sqlitePath = "testsync.db"
		}

		store, err := storage.NewSQLiteStore(sqlitePath)
		if err != nil {
			panic(err)
		}

		runs.SetDataStore(store)
	default:
		log.Warnf("Unknown storage type %q, defaulting to memory", conf.Storage.Type)
		runs.SetDataStore(storage.NewMemoryStore())
	}

	wsServer := ws.StartWebSocketServer(conf.WSPort)

	runs.SyncClient = conf.SyncClient
	ws.SyncClient = conf.SyncClient

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

	if err := runs.Store.Close(); err != nil {
		log.Errorf("Failed to close data store: %s", err.Error())
	}

	server.Shutdown(context.Background())   // nolint: gosec, errcheck
	wsServer.Shutdown(context.Background()) // nolint: gosec, errcheck

	log.Info("GOODBYE")
}
