package runs

import "sync"

// AllTests holds all registered tests (compatibility). Use helpers for access.
var (
	AllTests   = make(map[int]*Test)
	allTestsMu sync.RWMutex
)

// GetTest returns a test by ID.
func GetTest(id int) (*Test, bool) {
	allTestsMu.RLock()
	defer allTestsMu.RUnlock()

	t, ok := AllTests[id]
	return t, ok
}

// SetTest sets a test by ID.
func SetTest(id int, t *Test) {
	allTestsMu.Lock()
	defer allTestsMu.Unlock()

	AllTests[id] = t
}

// DeleteTest removes a test by ID.
func DeleteTest(id int) {
	allTestsMu.Lock()
	defer allTestsMu.Unlock()

	delete(AllTests, id)
}

// EnsureTest gets or creates a test by ID.
func EnsureTest(id int, create func() *Test) *Test {
	allTestsMu.Lock()
	defer allTestsMu.Unlock()

	if t, ok := AllTests[id]; ok {
		return t
	}

	created := create()
	AllTests[id] = created

	return created
}

// RangeTests iterates over a snapshot of tests.
func RangeTests(fn func(id int, t *Test)) {
	allTestsMu.RLock()
	snapshot := make(map[int]*Test, len(AllTests))
	for id, t := range AllTests {
		snapshot[id] = t
	}
	allTestsMu.RUnlock()

	for id, t := range snapshot {
		fn(id, t)
	}
}
