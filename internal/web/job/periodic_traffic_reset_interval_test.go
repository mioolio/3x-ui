package job

import (
	"testing"
	"time"
)

func TestIntervalResetDue(t *testing.T) {
	last := time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		period Period
		count  int
		now    time.Time
		want   bool
	}{
		{"hourly", 2, last.Add(119 * time.Minute), false},
		{"hourly", 2, last.Add(2 * time.Hour), true},
		{"daily", 2, time.Date(2026, 2, 1, 23, 0, 0, 0, time.UTC), false},
		{"daily", 2, time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC), true},
		{"weekly", 2, time.Date(2026, 2, 13, 23, 0, 0, 0, time.UTC), false},
		{"weekly", 2, time.Date(2026, 2, 14, 0, 0, 0, 0, time.UTC), true},
		{"monthly", 1, last, true},
		{"monthly", 2, time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC), false},
		{"monthly", 2, time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC), true},
		{"monthly", 2, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), true},
	} {
		if got := intervalResetDue(tc.period, tc.count, last.UnixMilli(), tc.now); got != tc.want {
			t.Errorf("%s/%d at %s: got %t, want %t", tc.period, tc.count, tc.now, got, tc.want)
		}
	}
}
