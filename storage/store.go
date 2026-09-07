package storage

import (
	"context"
	"time"
)

// DataStore defines persistence for test data. It is the single owner of test
// payloads: a run holds only coordination state, so the two can no longer
// disagree about what an agent stored (CODE-3).
//
// Every method takes a context so that a query is bounded by the request that
// asked for it: a client that gives up, or a request that hits the server's
// timeout, no longer leaves a query running against the database (STAB-10).
type DataStore interface {
	SaveData(ctx context.Context, testID int, data []byte) error
	LoadData(ctx context.Context, testID int) ([]byte, bool, error)
	DeleteData(ctx context.Context, testID int) error
	DeleteOlderThanExcept(ctx context.Context, limit time.Time, keepIDs []int) error

	// DataSize reports the size in bytes of one stored payload, without
	// reading it. The monitoring API reports sizes and never contents, so it
	// must be able to ask for one without pulling an arbitrary blob into
	// memory.
	DataSize(ctx context.Context, testID int) (int, bool, error)

	// DataSizes reports the size of every stored payload, keyed by test ID.
	// The run list needs all of them at once, and one query is cheaper than
	// one per run.
	DataSizes(ctx context.Context) (map[int]int, error)

	Close() error
}
