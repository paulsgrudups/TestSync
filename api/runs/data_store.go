package runs

import (
	"time"

	"github.com/paulsgrudups/testsync/storage"
)

// Store holds the active data store.
var Store storage.DataStore = storage.NewMemoryStore()

// SetDataStore sets the active data store.
func SetDataStore(store storage.DataStore) {
	if store == nil {
		return
	}

	Store = store
}

// SaveData persists test data.
func SaveData(testID int, data []byte) error {
	return Store.SaveData(testID, data)
}

// LoadData retrieves test data.
func LoadData(testID int) ([]byte, bool, error) {
	return Store.LoadData(testID)
}

// DeleteData removes test data.
func DeleteData(testID int) error {
	return Store.DeleteData(testID)
}

// DeleteDataOlderThan removes test data older than limit.
func DeleteDataOlderThan(limit time.Time) error {
	return Store.DeleteOlderThan(limit)
}
