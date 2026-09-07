package runs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/paulsgrudups/testsync/storage"
	"github.com/paulsgrudups/testsync/utils"
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
	log      *slog.Logger
}

// NewService creates a service backed by the given store and registry.
//
// The store is resolved once, here, rather than on every call: a service that
// is built without one can never acquire one later, so it is given a stand-in
// that reports [ErrNoDataStore] instead of panicking on first use.
func NewService(
	store storage.DataStore, registry *Registry, logger *slog.Logger,
) *Service {
	if store == nil {
		store = missingStore{}
	}

	if logger == nil {
		logger = utils.DiscardLogger()
	}

	if registry == nil {
		registry = NewRegistry(DefaultLimits(), logger)
	}

	return &Service{store: store, registry: registry, log: logger}
}

// Registry returns the registry this service works against.
func (s *Service) Registry() *Registry {
	return s.registry
}

// CreateTestData stores test data if it does not already exist. A payload
// larger than limits.max_data_bytes is refused with [ErrDataTooLarge], and a
// run that would take the server past limits.max_tests is refused with
// [ErrTestLimitReached]; neither is stored (STAB-3).
func (s *Service) CreateTestData(ctx context.Context, testID int, data []byte) error {
	if err := s.registry.Limits().checkDataSize(data); err != nil {
		return err
	}

	if _, ok := s.registry.Get(testID); ok {
		return ErrTestExists
	}

	if _, ok, err := s.store.LoadData(ctx, testID); err != nil {
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

	if err := s.store.SaveData(ctx, testID, data); err != nil {
		return err
	}

	return s.registerRun(testID)
}

// UpdateTestData stores test data regardless of existing state. The same two
// limits apply as for [Service.CreateTestData].
func (s *Service) UpdateTestData(ctx context.Context, testID int, data []byte) error {
	if err := s.registry.Limits().checkDataSize(data); err != nil {
		return err
	}

	if err := s.registry.CanAdmit(testID); err != nil {
		return err
	}

	if err := s.store.SaveData(ctx, testID, data); err != nil {
		return err
	}

	return s.registerRun(testID)
}

// ReadTestData returns test data or ErrTestNotFound.
//
// There is one place to look. This used to fall back to a copy cached on the
// run when the store had no row, which meant a payload could be served from
// either of two places that were written and reclaimed independently (CODE-3).
func (s *Service) ReadTestData(ctx context.Context, testID int) ([]byte, error) {
	data, ok, err := s.store.LoadData(ctx, testID)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, ErrTestNotFound
	}

	return data, nil
}

// DataSize reports the size in bytes of a run's stored payload, and whether
// there is one. It never reads the payload: the monitoring API reports sizes
// and counts, never contents.
func (s *Service) DataSize(ctx context.Context, testID int) (int, bool, error) {
	return s.store.DataSize(ctx, testID)
}

// DataSizes reports the size of every stored payload, keyed by test ID.
func (s *Service) DataSizes(ctx context.Context) (map[int]int, error) {
	return s.store.DataSizes(ctx)
}

// DeleteDataOlderThan removes test data older than limit, except for the runs
// in keep. The janitor keeps the runs whose agents are still connected, so a
// suite that is still running does not have its stored data deleted from
// under it (STAB-4).
func (s *Service) DeleteDataOlderThan(
	ctx context.Context, limit time.Time, keep []int,
) error {
	return s.store.DeleteOlderThanExcept(ctx, limit, keep)
}

// registerRun makes sure the run exists for a payload that has just been
// stored, so that agents can attach to it and the janitor can reclaim the two
// together. Creating one is refused once the server holds limits.max_tests
// runs.
func (s *Service) registerRun(testID int) error {
	_, err := s.registry.Ensure(testID)

	return err
}

// missingStore stands in for a store that was never supplied. It reports the
// misconfiguration on every call rather than letting a nil interface panic in
// a request goroutine.
type missingStore struct{}

func (missingStore) SaveData(context.Context, int, []byte) error { return ErrNoDataStore }

func (missingStore) LoadData(context.Context, int) ([]byte, bool, error) {
	return nil, false, ErrNoDataStore
}

func (missingStore) DeleteData(context.Context, int) error { return ErrNoDataStore }

func (missingStore) DataSize(context.Context, int) (int, bool, error) {
	return 0, false, ErrNoDataStore
}

func (missingStore) DataSizes(context.Context) (map[int]int, error) { return nil, ErrNoDataStore }

func (missingStore) DeleteOlderThanExcept(context.Context, time.Time, []int) error {
	return ErrNoDataStore
}

func (missingStore) Close() error { return nil }
