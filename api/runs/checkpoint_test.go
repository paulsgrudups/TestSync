package runs

import (
	"testing"
	"time"
)

func TestCheckpointTimeout(t *testing.T) {
	tests := map[string]struct {
		milliseconds int64
		want         time.Duration
	}{
		"missing field":  {milliseconds: 0, want: DefaultCheckpointTimeout},
		"negative":       {milliseconds: -1, want: DefaultCheckpointTimeout},
		"explicit value": {milliseconds: 250, want: 250 * time.Millisecond},
		"at the maximum": {
			milliseconds: MaxCheckpointTimeout.Milliseconds(),
			want:         MaxCheckpointTimeout,
		},
		"above the maximum": {
			milliseconds: MaxCheckpointTimeout.Milliseconds() * 10,
			want:         MaxCheckpointTimeout,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := CheckpointTimeout(tc.milliseconds); got != tc.want {
				t.Fatalf("CheckpointTimeout(%d) = %s, want %s", tc.milliseconds, got, tc.want)
			}
		})
	}
}
