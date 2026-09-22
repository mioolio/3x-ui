package service

import (
	"testing"
	"time"
)

func row(mod func(*throttleRow)) throttleRow {
	r := throttleRow{
		Email:           "user1",
		Enable:          true,
		SpeedLimitUp:    0,
		SpeedLimitDown:  0,
		DepletionAction: "",
		WindowMinutes:   0,
	}
	if mod != nil {
		mod(&r)
	}
	return r
}

func TestThrottleStateUnconfiguredMeansNoLimit(t *testing.T) {
	st := throttleStateFrom(row(nil), time.Now().UnixMilli())
	if lim, ok := st.EffectiveLimit(); ok {
		t.Fatalf("unconfigured client limited to %+v", lim)
	}
}

func TestThrottleStateAlwaysOnSpeedLimit(t *testing.T) {
	st := throttleStateFrom(row(func(r *throttleRow) {
		r.SpeedLimitDown, r.SpeedLimitUp = 2048, 512
	}), time.Now().UnixMilli())
	lim, ok := st.EffectiveLimit()
	if !ok || lim.DownKbps != 2048 || lim.UpKbps != 512 {
		t.Fatalf("limit = %+v ok=%v, want down 2048 up 512", lim, ok)
	}
	if st.DepletionThrottle() || st.WindowThrottle() {
		t.Fatal("spurious depletion/window throttle state")
	}
}

func TestThrottleStateDepletionDisableIsNotThrottled(t *testing.T) {
	now := time.Now().UnixMilli()
	st := throttleStateFrom(row(func(r *throttleRow) {
		r.Total, r.Up, r.Down = 1000, 600, 500
		r.DepletionAction = "disable"
	}), now)
	if st.DepletionThrottle() {
		t.Fatal("disable action produced a throttle")
	}
	if _, ok := st.EffectiveLimit(); ok {
		t.Fatal("depleted disable client got a rate cap")
	}
}

func TestThrottleStateDepletionThrottleCapsSpeed(t *testing.T) {
	now := time.Now().UnixMilli()
	st := throttleStateFrom(row(func(r *throttleRow) {
		r.Total, r.Up, r.Down = 1000, 600, 500
		r.DepletionAction, r.DepletionSpeed = "throttle", 1024
	}), now)
	if !st.DepletionThrottle() {
		t.Fatal("depleted throttle client not flagged")
	}
	lim, ok := st.EffectiveLimit()
	if !ok || lim.DownKbps != 1024 || lim.UpKbps != 1024 {
		t.Fatalf("limit = %+v, want 1024/1024", lim)
	}
}

func TestThrottleStateWindowOverrunOnlyInsideWindow(t *testing.T) {
	now := time.Now().UnixMilli()
	quota := int64(100 * 1024 * 1024)
	st := throttleStateFrom(row(func(r *throttleRow) {
		r.WindowQuotaGB, r.WindowMinutes = quota, 120
		r.WindowStarted = now - 60*60000
		r.WindowUsed = quota + 1
		r.WindowAction, r.WindowSpeed = "throttle", 256
	}), now)
	if !st.WindowExceeded() {
		t.Fatal("overrun inside window not detected")
	}
	lim, ok := st.EffectiveLimit()
	if !ok || lim.DownKbps != 256 {
		t.Fatalf("limit = %+v ok=%v, want 256", lim, ok)
	}

	// Same overrun, but the window already slid: no limit applies.
	st = throttleStateFrom(row(func(r *throttleRow) {
		r.WindowQuotaGB, r.WindowMinutes = quota, 120
		r.WindowStarted = now - 121*60000
		r.WindowUsed = quota + 1
		r.WindowAction, r.WindowSpeed = "throttle", 256
	}), now)
	if st.WindowExceeded() {
		t.Fatal("stale window overrun detected")
	}
	if _, ok := st.EffectiveLimit(); ok {
		t.Fatal("slid window still limits")
	}
}

func TestThrottleStateStrictestCapWins(t *testing.T) {
	now := time.Now().UnixMilli()
	st := throttleStateFrom(row(func(r *throttleRow) {
		r.SpeedLimitDown, r.SpeedLimitUp = 4096, 4096
		r.Total, r.Up, r.Down = 100, 100, 1
		r.DepletionAction, r.DepletionSpeed = "throttle", 1024
		r.WindowQuotaGB, r.WindowMinutes = 1<<30, 60
		r.WindowStarted = now - 30*60000
		r.WindowUsed = 1<<30 + 5
		r.WindowAction, r.WindowSpeed = "throttle", 256
	}), now)
	lim, ok := st.EffectiveLimit()
	if !ok || lim.DownKbps != 256 || lim.UpKbps != 256 {
		t.Fatalf("limit = %+v, want the strictest 256", lim)
	}
}

func TestStrictest(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{0, 0, 0},
		{0, 100, 100},
		{100, 0, 100},
		{100, 200, 100},
		{200, 100, 100},
	}
	for _, tt := range tests {
		if got := strictest(tt.a, tt.b); got != tt.want {
			t.Errorf("strictest(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestWindowEndTime(t *testing.T) {
	st := ThrottleLimitState{WindowStarted: 1000, WindowMinutes: 30}
	if got := st.WindowEndTime(); got != 1000+30*60000 {
		t.Fatalf("WindowEndTime = %d", got)
	}
	st.WindowMinutes = 0
	if got := st.WindowEndTime(); got != 0 {
		t.Fatalf("WindowEndTime without minutes = %d", got)
	}
}
