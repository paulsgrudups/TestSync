package runs

import (
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/paulsgrudups/testsync/utils"
)

// Janitor reclaims test runs that nobody is using any more, together with
// their stored data.
//
// It is started by the process, not by route registration: the sweep used to
// be a side effect of registering the HTTP routes, which meant one unstoppable
// goroutine per call to HandleRoutes and no way to configure or shut it down
// (STAB-5).
//
// A run with agents still connected is never swept, however old it is. The
// sweep used to delete a run purely by age, while its reader goroutines still
// held the old aggregate: the next agent to arrive created a fresh one, and
// the two halves of the suite then disagreed about connection counts and could
// not join each other's barriers (STAB-4).
type Janitor struct {
	interval  time.Duration
	retention time.Duration

	// registry and svc are the two halves of a run the janitor reclaims: the
	// in-memory aggregate and the stored payload. They are held explicitly so
	// that a janitor sweeps the server it was built for, and a test can sweep
	// its own registry without touching anyone else's (CODE-1).
	registry *Registry
	svc      *Service

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewJanitor creates a janitor that sweeps the given registry every interval
// and reclaims runs that have been idle for longer than retention, together
// with their data in svc's store. Interval and retention values that are not
// positive fall back to the defaults.
func NewJanitor(
	interval, retention time.Duration, registry *Registry, svc *Service,
) *Janitor {
	if interval <= 0 {
		interval = utils.DefaultCleanupInterval
	}

	if retention <= 0 {
		retention = utils.DefaultRetention
	}

	return &Janitor{
		interval:  interval,
		retention: retention,
		registry:  registry,
		svc:       svc,
	}
}

// Start runs the janitor until ctx is cancelled or [Janitor.Stop] is called.
// It sweeps once immediately, so that data left behind by a previous process
// is reclaimed at startup rather than one full interval later, and then on
// every tick. Calling it on a janitor that is already running does nothing.
func (j *Janitor) Start(ctx context.Context) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.cancel != nil {
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	j.cancel = cancel
	j.done = done

	go func() {
		defer utils.RecoverGoroutine("test cleanup")
		defer close(done)

		ticker := time.NewTicker(j.interval)
		defer ticker.Stop()

		j.Sweep(time.Now())

		for {
			select {
			case now := <-ticker.C:
				j.Sweep(now)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Stop ends the janitor and waits for its sweep to finish. It is safe to call
// on a janitor that was never started, and safe to call twice.
func (j *Janitor) Stop() {
	j.mu.Lock()

	cancel, done := j.cancel, j.done
	j.cancel, j.done = nil, nil

	j.mu.Unlock()

	if cancel == nil {
		return
	}

	cancel()
	<-done
}

// Sweep reclaims everything that expired by now. It is exported so that an
// operator-facing trigger, and the tests, can run one sweep without waiting
// for a tick.
//
// Runs with connected agents are kept, and so is their stored data: a suite
// that is still running must never have its state deleted underneath it.
func (j *Janitor) Sweep(now time.Time) {
	limit := now.Add(-j.retention)

	keep := make([]int, 0)

	j.registry.Range(func(testID int, t *Test) {
		if !t.Created.Before(limit) {
			keep = append(keep, testID)
			return
		}

		if count := t.ConnectionCount(); count > 0 {
			log.WithFields(log.Fields{
				"test_id":     testID,
				"connections": count,
			}).Debug("Keeping expired test with connected agents")

			keep = append(keep, testID)

			return
		}

		log.WithField("test_id", testID).Info("Deleting expired test")
		j.registry.Delete(testID)
	})

	if err := j.svc.DeleteDataOlderThan(limit, keep); err != nil {
		log.Errorf("Failed to delete old data: %s", err.Error())
	}
}
