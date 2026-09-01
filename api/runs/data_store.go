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

// DeleteDataOlderThan removes test data older than limit, except for the runs
// in keep. The janitor keeps the runs whose agents are still connected, so a
// suite that is still running does not have its stored data deleted from
// under it (STAB-4).
func DeleteDataOlderThan(limit time.Time, keep []int) error {
	if Store == nil {
		return ErrNoDataStore
	}

	return Store.DeleteOlderThanExcept(limit, keep)
}
