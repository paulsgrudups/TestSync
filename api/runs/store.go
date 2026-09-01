package runs

import (
	"fmt"
	"maps"
	"sync"
)

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

// EnsureTest gets or creates a test by ID. Creating one is refused with
// [ErrTestLimitReached] once the registry holds limits.max_tests runs: the
// registry used to grow by one entry per test ID ever seen, which any client
// could turn into an out-of-memory kill (STAB-3).
//
// The check and the insert happen under the same lock, so concurrent agents
// cannot race past the limit together.
func EnsureTest(id int, create func() *Test) (*Test, error) {
	allTestsMu.Lock()
	defer allTestsMu.Unlock()

	if t, ok := AllTests[id]; ok {
		return t, nil
	}

	if err := testLimitLocked(); err != nil {
		return nil, err
	}

	created := create()
	AllTests[id] = created

	return created, nil
}

// CanAdmitTest reports whether a run for this ID could be registered right
// now. It is advisory: it lets a WebSocket registration be refused with an
// HTTP status before the connection is upgraded, while [EnsureTest] remains
// the enforcement point.
func CanAdmitTest(id int) error {
	allTestsMu.RLock()
	defer allTestsMu.RUnlock()

	if _, ok := AllTests[id]; ok {
		return nil
	}

	return testLimitLocked()
}

// TestCount returns how many runs are registered.
func TestCount() int {
	allTestsMu.RLock()
	defer allTestsMu.RUnlock()

	return len(AllTests)
}

// testLimitLocked reports whether one more run may be registered. It must be
// called with allTestsMu held, in either mode.
func testLimitLocked() error {
	limit := CurrentLimits().MaxTests
	if limit <= 0 || len(AllTests) < limit {
		return nil
	}

	return fmt.Errorf(
		"%w: %d runs are registered, which is the configured maximum",
		ErrTestLimitReached, len(AllTests),
	)
}

// RangeTests iterates over a snapshot of tests.
func RangeTests(fn func(id int, t *Test)) {
	allTestsMu.RLock()
	snapshot := make(map[int]*Test, len(AllTests))
	maps.Copy(snapshot, AllTests)
	allTestsMu.RUnlock()

	for id, t := range snapshot {
		fn(id, t)
	}
}
