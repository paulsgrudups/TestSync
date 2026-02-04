package storage

import (
	"sync"
	"time"
)

type memoryRecord struct {
	data    []byte
	created int64
}

// MemoryStore keeps test data in memory.
type MemoryStore struct {
	mu   sync.RWMutex
	data map[int]memoryRecord
}

// NewMemoryStore creates an in-memory data store.
func NewMemoryStore() DataStore {
	return &MemoryStore{data: make(map[int]memoryRecord)}
}

func (m *MemoryStore) SaveData(testID int, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	copyData := make([]byte, len(data))
	copy(copyData, data)

	m.data[testID] = memoryRecord{data: copyData, created: time.Now().UnixMilli()}
	return nil
}

func (m *MemoryStore) LoadData(testID int) ([]byte, bool, error) {
	m.mu.RLock()
	rec, ok := m.data[testID]
	m.mu.RUnlock()

	if !ok {
		return nil, false, nil
	}

	copyData := make([]byte, len(rec.data))
	copy(copyData, rec.data)

	return copyData, true, nil
}

func (m *MemoryStore) DeleteData(testID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, testID)
	return nil
}

func (m *MemoryStore) DeleteOlderThan(limit time.Time) error {
	limitUnix := limit.UnixMilli()

	m.mu.Lock()
	defer m.mu.Unlock()

	for id, rec := range m.data {
		if rec.created < limitUnix {
			delete(m.data, id)
		}
	}

	return nil
}

func (m *MemoryStore) Close() error {
	return nil
}
