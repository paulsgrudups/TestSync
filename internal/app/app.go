// Package app wires one TestSync server instance together.
//
// It exists so that the server has a single explicit construction point.
// Everything the request paths need used to live in package-level variables
// installed by four different setters, and the process only worked because
// startup happened to call them in the right order: moving one line in main
// could start the server with authentication disabled (CODE-1, SEC-1). Here
// the dependencies are fields, so a server that is missing one does not
// compile, and two independent servers can exist in one process, which is
// what an integration test needs (TEST-2).
package app

import (
	"log/slog"
	"time"

	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/api/runs"
	"github.com/paulsgrudups/testsync/internal/metrics"
	"github.com/paulsgrudups/testsync/storage"
	"github.com/paulsgrudups/testsync/utils"
)

// App holds everything one server instance owns. Its fields are set once, by
// [New], and read from every request goroutine, so nothing here may be
// reassigned after construction.
type App struct {
	// Config is the operator's configuration, already defaulted and
	// validated.
	Config utils.Config

	// Store is the persistent home of test payloads.
	Store storage.DataStore

	// Registry holds the live test runs and the limits enforced on them.
	Registry *runs.Registry

	// Service is the operation layer over Store and Registry.
	Service *runs.Service

	// Auth is the single validator both the HTTP and the WebSocket server
	// authenticate through, so the two paths cannot drift apart (SEC-1).
	Auth *auth.Validator

	// Metrics holds the counters the server maintains. The gauges are read
	// from the Registry when the metrics are scraped.
	Metrics *Metrics

	// Log is this server's logger. Components derive their own from it with
	// [slog.Logger.With], so a line about a barrier already names the run it
	// belongs to (CODE-6).
	Log *slog.Logger
}

// New builds an application from its external dependencies: the operator's
// configuration, the opened data store, the credential validator and the
// process logger. The registry and the service are derived from them. A nil
// logger discards, so a test that does not care about output says nothing.
func New(
	conf utils.Config,
	store storage.DataStore,
	validator *auth.Validator,
	logger *slog.Logger,
) *App {
	if logger == nil {
		logger = utils.DiscardLogger()
	}

	registry := runs.NewRegistry(
		runs.LimitsFromConfig(conf.Limits), logger,
		runs.WithReleaseLeadTime(time.Duration(conf.Checkpoint.ReleaseLeadTime)),
	)

	return &App{
		Config:   conf,
		Store:    store,
		Registry: registry,
		Service:  runs.NewService(store, registry, logger),
		Auth:     validator,
		Metrics:  NewMetrics(),
		Log:      logger,
	}
}

// Metrics are the counters one server instance maintains.
type Metrics struct {
	// Requests counts HTTP requests by route template and status code.
	Requests *metrics.CounterVec

	// Commands counts WebSocket commands by name and outcome: "ok", or the
	// code of the error the client was sent.
	Commands *metrics.CounterVec
}

// NewMetrics creates an empty set of counters.
func NewMetrics() *Metrics {
	return &Metrics{
		Requests: metrics.NewCounterVec(
			"testsync_requests_total",
			"HTTP requests served, by route template and status code.",
			"route", "code",
		),
		Commands: metrics.NewCounterVec(
			"testsync_websocket_commands_total",
			"WebSocket commands handled, by command and outcome.",
			"command", "outcome",
		),
	}
}
