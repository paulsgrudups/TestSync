package runs

import (
	"errors"
	"fmt"

	"github.com/paulsgrudups/testsync/utils"
)

// Limit rejections. Nothing bounded the number of runs, the agents attached to
// one run, the barriers one run could create, or the size of a stored payload,
// so any authorized client could exhaust the server's memory (STAB-3, SEC-8).
// Each of these is reported to the client that hit it; none of them is a
// silent drop.
var (
	// ErrTestLimitReached means the server already holds limits.max_tests
	// runs. Reclaiming a run needs its agents to disconnect and its retention
	// window to pass.
	ErrTestLimitReached = errors.New("test run limit reached")

	// ErrConnectionLimitReached means the run already has
	// limits.max_connections_per_test agents attached.
	ErrConnectionLimitReached = errors.New("connection limit reached for this test run")

	// ErrCheckpointLimitReached means the run already has
	// limits.max_checkpoints_per_test distinct checkpoint identifiers.
	// Barriers are reusable, so a looping suite needs one identifier per
	// barrier rather than one per iteration.
	ErrCheckpointLimitReached = errors.New("checkpoint limit reached for this test run")

	// ErrDataTooLarge means the payload is larger than limits.max_data_bytes.
	ErrDataTooLarge = errors.New("test data too large")
)

// Limits bounds what one server will hold. The zero value bounds nothing, so
// a [Registry] is built from [DefaultLimits] or from the operator's
// configuration through [LimitsFromConfig].
type Limits struct {
	// MaxTests is how many test runs may be registered at once.
	MaxTests int

	// MaxConnectionsPerTest is how many agents may attach to one run.
	MaxConnectionsPerTest int

	// MaxCheckpointsPerTest is how many distinct checkpoint identifiers one
	// run may create.
	MaxCheckpointsPerTest int

	// MaxDataBytes bounds a stored payload. The HTTP body cap and the
	// WebSocket frame cap are taken from the same number, so a payload that is
	// accepted by one path is accepted by the other.
	MaxDataBytes int64
}

// DefaultLimits returns the limits used when the operator configured none.
func DefaultLimits() Limits {
	return Limits{
		MaxTests:              utils.DefaultMaxTests,
		MaxConnectionsPerTest: utils.DefaultMaxConnectionsPerTest,
		MaxCheckpointsPerTest: utils.DefaultMaxCheckpointsPerTest,
		MaxDataBytes:          utils.DefaultMaxDataBytes,
	}
}

// LimitsFromConfig turns the operator's configuration into the limits the
// server enforces. A field left at zero keeps its default.
func LimitsFromConfig(conf utils.LimitsConfig) Limits {
	limits := DefaultLimits()

	if conf.MaxTests > 0 {
		limits.MaxTests = conf.MaxTests
	}

	if conf.MaxConnectionsPerTest > 0 {
		limits.MaxConnectionsPerTest = conf.MaxConnectionsPerTest
	}

	if conf.MaxCheckpointsPerTest > 0 {
		limits.MaxCheckpointsPerTest = conf.MaxCheckpointsPerTest
	}

	if conf.MaxDataBytes > 0 {
		limits.MaxDataBytes = conf.MaxDataBytes
	}

	return limits
}

// checkDataSize reports whether a payload may be stored under these limits.
func (l Limits) checkDataSize(data []byte) error {
	if l.MaxDataBytes <= 0 || int64(len(data)) <= l.MaxDataBytes {
		return nil
	}

	return fmt.Errorf(
		"%w: %d bytes exceeds the %d byte limit",
		ErrDataTooLarge, len(data), l.MaxDataBytes,
	)
}
