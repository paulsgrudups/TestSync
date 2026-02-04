package runs

import (
	"sync"
	"time"

	"github.com/paulsgrudups/testsync/wsutil"
	log "github.com/sirupsen/logrus"
)

// Checkpoint describes a single checkpoint instance.
type Checkpoint struct {
	Identifier    string
	TargetCount   int
	ConnectionIdx []int
	Finished      bool
	connEvents    chan bool
	mu            sync.Mutex
}

// CreateCheckpoint create a new checkpoint for specified test.
func CreateCheckpoint(identifier string, target int, t *Test) *Checkpoint {
	log.Infof("Creating new checkpoint %q", identifier)

	cp := &Checkpoint{
		Identifier:  identifier,
		TargetCount: target,
		connEvents:  make(chan bool),
	}

	go func() {
		for range cp.connEvents {
			log.Info("Got event, checking!")
			cp.mu.Lock()
			finished := len(cp.ConnectionIdx) >= cp.TargetCount
			if finished {
				log.Debug("Connection target reached - broadcasting")
				cp.Finished = true
			}
			cp.mu.Unlock()

			if finished {
				cp.broadcastStatus(t)
				break
			}
		}
	}()

	return cp
}

// AddConnection adds connection index to checkpoint.
func (cp *Checkpoint) AddConnection(idx int) {
	log.Debugf("Adding connection to checkpoint %q", cp.Identifier)

	cp.mu.Lock()
	cp.ConnectionIdx = append(cp.ConnectionIdx, idx)
	finished := cp.Finished
	cp.mu.Unlock()

	if !finished {
		cp.connEvents <- true
	}
}

// IsFinished returns whether checkpoint has completed.
func (cp *Checkpoint) IsFinished() bool {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	return cp.Finished
}

func (cp *Checkpoint) broadcastStatus(t *Test) {
	cp.mu.Lock()
	indices := make([]int, len(cp.ConnectionIdx))
	copy(indices, cp.ConnectionIdx)
	finished := cp.Finished
	cp.mu.Unlock()

	connections := t.GetConnectionsSnapshot()

	for _, idx := range indices {
		if idx < 0 || idx >= len(connections) {
			continue
		}

		err := wsutil.SendMessage(
			connections[idx],
			"wait_checkpoint",
			struct {
				Identifier string `json:"identifier"`
				Finished   bool   `json:"finished"`
				StartAt    int64  `json:"start_at"`
			}{
				Identifier: cp.Identifier,
				Finished:   finished,
				StartAt:    time.Now().Add(time.Millisecond * 500).UnixMilli(),
			},
		)
		if err != nil {
			log.Errorf(
				"Could not broadcast message to checkpoint %q: %s",
				cp.Identifier, err.Error(),
			)
		}
	}
}
