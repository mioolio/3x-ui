package service

import "fmt"

const maxSpeedLimitKbps int64 = 1_000_000_000

type directionalRate struct {
	UpKbps   int64 `json:"upKbps"`
	DownKbps int64 `json:"downKbps"`
}

func equalOptionalInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// A nil direction inherits the old symmetric rate. A present zero explicitly
// makes that direction unlimited at this policy layer.
func effectiveDirectionalRate(legacy int64, up, down *int64) (directionalRate, bool) {
	if up == nil && down == nil {
		return directionalRate{}, false
	}
	rate := directionalRate{UpKbps: legacy, DownKbps: legacy}
	if up != nil {
		rate.UpKbps = *up
	}
	if down != nil {
		rate.DownKbps = *down
	}
	return rate, true
}

func validateDirectionalRate(scope string, up, down *int64) error {
	for _, entry := range []struct {
		name  string
		value *int64
	}{{"speedLimitUpKbps", up}, {"speedLimitDownKbps", down}} {
		if entry.value != nil && (*entry.value < 0 || *entry.value > maxSpeedLimitKbps) {
			return fmt.Errorf("%s %s must be between 0 and %d", scope, entry.name, maxSpeedLimitKbps)
		}
	}
	return nil
}
