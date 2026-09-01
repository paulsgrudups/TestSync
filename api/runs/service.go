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
)

// Service provides higher level operations for test data.
type Service struct {
	dataStore storage.DataStore
}

// DefaultService is the package-level service used by handlers. SetDataStore
// rebinds it once the store exists.
var DefaultService = NewService(nil)

// NewService creates a service backed by the provided store. A nil store falls
// back to the package-level Store, resolved once here rather than on every
// call: a service that resolves to no store cannot acquire one later.
func NewService(store storage.DataStore) *Service {
	if store == nil {
		store = Store
	}

	return &Service{dataStore: store}
}

// CreateTestData stores test data if it does not already exist. A payload
// larger than limits.max_data_bytes is refused with [ErrDataTooLarge], and a
// run that would take the server past limits.max_tests is refused with
// [ErrTestLimitReached]; neither is stored (STAB-3).
func (s *Service) CreateTestData(testID int, data []byte) error {
	if err := checkDataSize(data); err != nil {
		return err
	}

	if _, ok := GetTest(testID); ok {
		return ErrTestExists
	}

	if _, ok, err := s.dataStore.LoadData(testID); err != nil {
		return err
	} else if ok {
		return ErrTestExists
	}

	// Checked before the payload is written, so that a run refused for being
	// one too many does not leave a row behind. EnsureTest below is still the
	// enforcement point: it decides under the registry's own lock.
	if err := CanAdmitTest(testID); err != nil {
		return err
	}

	if err := s.dataStore.SaveData(testID, data); err != nil {
		return err
	}

	return registerData(testID, data)
}

// UpdateTestData stores test data regardless of existing state. The same two
// limits apply as for [Service.CreateTestData].
func (s *Service) UpdateTestData(testID int, data []byte) error {
	if err := checkDataSize(data); err != nil {
		return err
	}

	if err := CanAdmitTest(testID); err != nil {
		return err
	}

	if err := s.dataStore.SaveData(testID, data); err != nil {
		return err
	}

	return registerData(testID, data)
}

// ReadTestData returns test data or ErrTestNotFound.
func (s *Service) ReadTestData(testID int) ([]byte, error) {
	data, ok, err := s.dataStore.LoadData(testID)
	if err != nil {
		return nil, err
	}
	if ok {
		return data, nil
	}

	if m, exists := GetTest(testID); exists {
		data = m.GetData()
		if len(data) > 0 {
			return data, nil
		}
	}

	return nil, ErrTestNotFound
}

// registerData records the payload against its run, creating the run when it
// is new. Creating one is refused once the server holds limits.max_tests runs.
func registerData(testID int, data []byte) error {
	t, err := EnsureTest(testID, func() *Test {
		return &Test{Created: nowUTC(), Data: data}
	})
	if err != nil {
		return err
	}

	t.SetData(data)

	return nil
}

func nowUTC() time.Time {
	return time.Now().UTC()
}
