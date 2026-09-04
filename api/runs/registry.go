package runs

import (
	"fmt"
	"maps"
	"sync"
	"time"
)

// Registry holds every test run one server knows about, together with the
// limits that server enforces on them.
//
// It replaces the package-level map that used to hold the runs. That map was
// exported, so every test replaced it wholesale without holding its mutex
// while the cleanup goroutine could be ranging over it (CONC-12); the limits
// lived in a second global installed by a third function, so the process only
// worked because startup happened to call them in the right order (CODE-1);
// and two independent servers could not exist in one process, which is what an
// integration test needs (TEST-2).
type Registry struct {
	// limits is fixed at construction. Every run created here is bounded by
	// the same numbers, so enforcing a limit never means reading shared
	// mutable state from a connection goroutine.
	limits Limits

	mu    sync.RWMutex
	tests map[int]*Test
}

// NewRegistry creates an empty registry that enforces the given limits. Pass
// [DefaultLimits] when the operator configured none: a registry whose limits
// are the zero value enforces nothing at all.
func NewRegistry(limits Limits) *Registry {
	return &Registry{limits: limits, tests: make(map[int]*Test)}
}

// Limits returns the limits this registry enforces. They are fixed at
// construction, so the value needs no lock and cannot change under a caller
// that has already read it.
func (reg *Registry) Limits() Limits {
	return reg.limits
}

// Get returns a run by ID, and whether it is registered.
func (reg *Registry) Get(id int) (*Test, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	t, ok := reg.tests[id]

	return t, ok
}

// Delete removes a run by ID. The run's stored data is not touched: that is
// the [Janitor]'s to reclaim, in the same sweep and under the same rules.
func (reg *Registry) Delete(id int) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	delete(reg.tests, id)
}

// Ensure returns the run for id, creating it the first time the ID is seen.
// Creating one is refused with [ErrTestLimitReached] once the registry holds
// limits.max_tests runs: the registry used to grow by one entry per test ID
// ever seen, which any client could turn into an out-of-memory kill (STAB-3).
//
// The check and the insert happen under the same lock, so concurrent agents
// cannot race past the limit together.
func (reg *Registry) Ensure(id int) (*Test, error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	if t, ok := reg.tests[id]; ok {
		return t, nil
	}

	if err := reg.admitLocked(); err != nil {
		return nil, err
	}

	created := &Test{Created: time.Now().UTC(), limits: reg.limits}
	reg.tests[id] = created

	return created, nil
}

// CanAdmit reports whether a run for this ID could be registered right now. It
// is advisory: it lets a WebSocket registration be refused with an HTTP status
// before the connection is upgraded, while [Registry.Ensure] remains the
// enforcement point.
func (reg *Registry) CanAdmit(id int) error {
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	if _, ok := reg.tests[id]; ok {
		return nil
	}

	return reg.admitLocked()
}

// Count returns how many runs are registered.
func (reg *Registry) Count() int {
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	return len(reg.tests)
}

// Range calls fn for every registered run. It iterates over a snapshot, so fn
// may register or delete runs without deadlocking against the registry's own
// lock.
func (reg *Registry) Range(fn func(id int, t *Test)) {
	reg.mu.RLock()
	snapshot := make(map[int]*Test, len(reg.tests))
	maps.Copy(snapshot, reg.tests)
	reg.mu.RUnlock()

	for id, t := range snapshot {
		fn(id, t)
	}
}

// admitLocked reports whether one more run may be registered. It must be
// called with reg.mu held, in either mode.
func (reg *Registry) admitLocked() error {
	limit := reg.limits.MaxTests
	if limit <= 0 || len(reg.tests) < limit {
		return nil
	}

	return fmt.Errorf(
		"%w: %d runs are registered, which is the configured maximum",
		ErrTestLimitReached, len(reg.tests),
	)
}
