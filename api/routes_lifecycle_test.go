package api

import (
	"runtime"
	"testing"
	"time"
)

// TestHandleRoutesSpawnsNoGoroutines covers STAB-5: registering routes must
// have no side effects. Route registration used to start a cleanup ticker that
// could never be stopped, so every call leaked a goroutine and calling
// HandleRoutes more than once was quietly unsafe.
func TestHandleRoutesSpawnsNoGoroutines(t *testing.T) {
	const calls = 100

	settle()

	before := runtime.NumGoroutine()

	for range calls {
		if _, err := HandleRoutes(); err != nil {
			t.Fatalf("failed to create router: %v", err)
		}
	}

	settle()

	if leaked := runtime.NumGoroutine() - before; leaked > 0 {
		t.Fatalf(
			"%d HandleRoutes calls leaked %d goroutines", calls, leaked,
		)
	}
}

// settle waits for goroutines that are already on their way out to finish, so
// that a count taken afterwards is not measuring the previous test.
func settle() {
	for range 20 {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
}
