package runs

import (
	"errors"
	"time"

	"github.com/paulsgrudups/testsync/storage"
)

// ErrNoDataStore indicates the data store has not been initialised. Callers
// must invoke SetDataStore during startup before serving any request.
var ErrNoDataStore = errors.New("data store not initialised")

// Store holds the active data store. It is nil until SetDataStore is called.
var Store storage.DataStore

// SetDataStore sets the active data store and rebinds DefaultService to it.
// It must be called during startup, before any route is registered: services
// resolve their store once, at construction, so anything built earlier would
// hold a nil store for the process lifetime.
func SetDataStore(store storage.DataStore) {
	if store == nil {
		return
	}

	Store = store
	DefaultService = NewService(store)
}

// DeleteDataOlderThan removes test data older than limit. It is called from
// the background cleanup ticker, which can outlive a failed startup.
func DeleteDataOlderThan(limit time.Time) error {
	if Store == nil {
		return ErrNoDataStore
	}

	return Store.DeleteOlderThan(limit)
}
