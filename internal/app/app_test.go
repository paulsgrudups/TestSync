package app

import (
	"testing"
	"time"

	"github.com/paulsgrudups/testsync/utils"
)

// TestNewAppliesTheReleaseLeadTime covers the wiring from
// checkpoint.release_lead_time to the registry the barriers read it from.
func TestNewAppliesTheReleaseLeadTime(t *testing.T) {
	t.Parallel()

	conf := utils.Config{}
	utils.ApplyDefaults(&conf)
	conf.Checkpoint.ReleaseLeadTime = utils.Duration(2 * time.Second)

	a := New(conf, nil, nil, nil)

	if got := a.Registry.ReleaseLeadTime(); got != 2*time.Second {
		t.Fatalf("expected a 2s lead time, got %s", got)
	}
}
