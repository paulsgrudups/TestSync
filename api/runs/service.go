package runs

import (
	"errors"
	"time"

	"github.com/paulsgrudups/testsync/storage"
)

var (
	// ErrTestExists indicates test data already exists.
	ErrTestExists = errors.New("test data already exists")
	// ErrTestNotFound indicates test data not found.
	ErrTestNotFound = errors.New("test data not found")
	// ErrNoDataStore indicates the service was built without a data store.
	// Every operation that would touch storage reports it rather than
	// dereferencing a nil interface.
	ErrNoDataStore = errors.New("data store not initialised")
)

// Service provides the higher level operations for test data. It owns the
// data store and the registry it works against, so a handler never has to
// resolve either from package state (CODE-1).
type Service struct {
	store    storage.DataStore
	registry *Registry
}

// NewService creates a service backed by the given store and registry.
//
// The store is resolved once, here, rather than on every call: a service that
// is built without one can never acquire one later, so it is given a stand-in
// that reports [ErrNoDataStore] instead of panicking on first use.
func NewService(store storage.DataStore, registry *Registry) *Service {
	if store == nil {
		store = missingStore{}
	}

	if registry == nil {
		registry = NewRegistry(DefaultLimits())
	}

	return &Service{store: store, registry: registry}
}

// Registry returns the registry this service works against.
func (s *Service) Registry() *Registry {
	return s.registry
}

// CreateTestData stores test data if it does not already exist. A payload
// larger than limits.max_data_bytes is refused with [ErrDataTooLarge], and a
// run that would take the server past limits.max_tests is refused with
// [ErrTestLimitReached]; neither is stored (STAB-3).
func (s *Service) CreateTestData(testID int, data []byte) error {
	if err := s.registry.Limits().checkDataSize(data); err != nil {
		return err
	}

	if _, ok := s.registry.Get(testID); ok {
		return ErrTestExists
	}

	if _, ok, err := s.store.LoadData(testID); err != nil {
		return err
	} else if ok {
		return ErrTestExists
	}

	// Checked before the payload is written, so that a run refused for being
	// one too many does not leave a row behind. Registry.Ensure below is still
	// the enforcement point: it decides under the registry's own lock.
	if err := s.registry.CanAdmit(testID); err != nil {
		return err
	}

	if err := s.store.SaveData(testID, data); err != nil {
		return err
	}

	return s.registerData(testID, data)
}

// UpdateTestData stores test data regardless of existing state. The same two
// limits apply as for [Service.CreateTestData].
func (s *Service) UpdateTestData(testID int, data []byte) error {
	if err := s.registry.Limits().checkDataSize(data); err != nil {
		return err
	}

	if err := s.registry.CanAdmit(testID); err != nil {
		return err
	}

	if err := s.store.SaveData(testID, data); err != nil {
		return err
	}

	return s.registerData(testID, data)
}

// ReadTestData returns test data or ErrTestNotFound.
func (s *Service) ReadTestData(testID int) ([]byte, error) {
	data, ok, err := s.store.LoadData(testID)
	if err != nil {
		return nil, err
	}

	if ok {
		return data, nil
	}

	if t, exists := s.registry.Get(testID); exists {
		data = t.GetData()
		if len(data) > 0 {
			return data, nil
		}
	}

	return nil, ErrTestNotFound
}

// DeleteDataOlderThan removes test data older than limit, except for the runs
// in keep. The janitor keeps the runs whose agents are still connected, so a
// suite that is still running does not have its stored data deleted from
// under it (STAB-4).
func (s *Service) DeleteDataOlderThan(limit time.Time, keep []int) error {
	return s.store.DeleteOlderThanExcept(limit, keep)
}

// registerData records the payload against its run, creating the run when it
// is new. Creating one is refused once the server holds limits.max_tests runs.
func (s *Service) registerData(testID int, data []byte) error {
	t, err := s.registry.Ensure(testID)
	if err != nil {
		return err
	}

	t.SetData(data)

	return nil
}

// missingStore stands in for a store that was never supplied. It reports the
// misconfiguration on every call rather than letting a nil interface panic in
// a request goroutine.
type missingStore struct{}

func (missingStore) SaveData(int, []byte) error { return ErrNoDataStore }

func (missingStore) LoadData(int) ([]byte, bool, error) { return nil, false, ErrNoDataStore }

func (missingStore) DeleteData(int) error { return ErrNoDataStore }

func (missingStore) DeleteOlderThanExcept(time.Time, []int) error { return ErrNoDataStore }

func (missingStore) Close() error { return nil }
