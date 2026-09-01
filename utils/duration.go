package utils

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Duration is a [time.Duration] that is configured as a string, so that a
// retention window reads as "12h" in configuration.json rather than as a
// number whose unit the operator has to guess.
type Duration time.Duration

// MarshalJSON writes the duration in the same string form it is read from.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Duration returns the configured value.
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

// UnmarshalJSON reads a duration string such as "12h", "90s" or "500ms". An
// empty string means "not configured" and leaves the default in place; a
// value that is not a duration is an error the operator can act on, rather
// than a silently ignored field.
func (d *Duration) UnmarshalJSON(body []byte) error {
	var raw string
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf(
			"duration must be a string such as \"12h\" or \"90s\": %w", err,
		)
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		*d = 0
		return nil
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf(
			"invalid duration %q: use a value such as \"12h\", \"90s\" or \"500ms\"",
			raw,
		)
	}

	if parsed <= 0 {
		return fmt.Errorf("duration %q must be positive", raw)
	}

	*d = Duration(parsed)

	return nil
}
