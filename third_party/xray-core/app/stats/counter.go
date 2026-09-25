package stats

import (
	"math"
	"sync/atomic"
)

// Counter is an implementation of stats.Counter.
type Counter struct {
	value int64
}

// Value implements stats.Counter.
func (c *Counter) Value() int64 {
	return atomic.LoadInt64(&c.value)
}

// Set implements stats.Counter.
func (c *Counter) Set(newValue int64) int64 {
	return atomic.SwapInt64(&c.value, newValue)
}

// Add implements stats.Counter.
func (c *Counter) Add(delta int64) int64 {
	for {
		old := atomic.LoadInt64(&c.value)
		next := old + delta
		if delta > 0 && old > math.MaxInt64-delta {
			next = math.MaxInt64
		} else if delta < 0 && old < math.MinInt64-delta {
			next = math.MinInt64
		}
		if atomic.CompareAndSwapInt64(&c.value, old, next) {
			return next
		}
	}
}
