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
	"github.com/paulsgrudups/testsync/api/auth"
	"github.com/paulsgrudups/testsync/api/runs"
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
}

// New builds an application from its three external dependencies: the
// operator's configuration, the opened data store, and the credential
// validator. The registry and the service are derived from them.
func New(conf utils.Config, store storage.DataStore, validator *auth.Validator) *App {
	registry := runs.NewRegistry(runs.LimitsFromConfig(conf.Limits))

	return &App{
		Config:   conf,
		Store:    store,
		Registry: registry,
		Service:  runs.NewService(store, registry),
		Auth:     validator,
	}
}
