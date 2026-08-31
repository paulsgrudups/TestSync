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

// CreateTestData stores test data if it does not already exist.
func (s *Service) CreateTestData(testID int, data []byte) error {
	if _, ok := GetTest(testID); ok {
		return ErrTestExists
	}

	if _, ok, err := s.dataStore.LoadData(testID); err != nil {
		return err
	} else if ok {
		return ErrTestExists
	}

	if err := s.dataStore.SaveData(testID, data); err != nil {
		return err
	}

	if t, ok := GetTest(testID); ok {
		t.SetData(data)
	} else {
		SetTest(testID, &Test{
			Created:     nowUTC(),
			Data:        data,
			CheckPoints: make(map[string]*Checkpoint),
		})
	}

	return nil
}

// UpdateTestData stores test data regardless of existing state.
func (s *Service) UpdateTestData(testID int, data []byte) error {
	if err := s.dataStore.SaveData(testID, data); err != nil {
		return err
	}

	if t, ok := GetTest(testID); ok {
		t.SetData(data)
	} else {
		SetTest(testID, &Test{
			Created:     nowUTC(),
			Data:        data,
			CheckPoints: make(map[string]*Checkpoint),
		})
	}

	return nil
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

func nowUTC() time.Time {
	return time.Now().UTC()
}
