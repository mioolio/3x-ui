package throttle

import (
	"sync"
	"time"
)

// KbpsToBytesPerSec converts a kilobits-per-second limit into relay bytes.
func KbpsToBytesPerSec(kbps int) int64 {
	if kbps <= 0 {
		return 0
	}
	return int64(kbps) * 1000 / 8
}

// bucket is a token bucket shared by every flow of one client in one
// direction, so N concurrent connections still get the configured rate in
// total. A nil bucket or a non-positive rate is unlimited.
type bucket struct {
	mu     sync.Mutex
	rate   int64 // bytes per second
	cap    int64
	tokens int64
	last   time.Time
}

const minBucketCapacity = 64 * 1024

func newBucket(rateBps int64) *bucket {
	if rateBps <= 0 {
		return nil
	}
	capacity := max(2*rateBps, int64(minBucketCapacity))
	return &bucket{rate: rateBps, cap: capacity, tokens: capacity, last: time.Now()}
}

// setRate retunes the bucket, carrying leftover tokens across the change so an
// operator raising a limit does not hand out a fresh burst.
func (b *bucket) setRate(rateBps int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked(time.Now())
	if rateBps <= 0 {
		b.rate = 0
		return
	}
	b.rate = rateBps
	b.cap = max(2*rateBps, int64(minBucketCapacity))
	if b.tokens > b.cap {
		b.tokens = b.cap
	}
}

// wait blocks until n bytes fit the bucket. It re-checks the rate after every
// sleep so a live SetLimits takes effect within a second. Tokens are never
// zeroed mid-wait: capping the sleep below the full deficit would otherwise
// reset progress every round and starve large waits forever.
func (b *bucket) wait(n int64) {
	if b == nil || n <= 0 {
		return
	}
	for {
		b.mu.Lock()
		now := time.Now()
		b.refillLocked(now)
		if b.rate <= 0 {
			b.mu.Unlock()
			return
		}
		if b.tokens >= n {
			b.tokens -= n
			b.mu.Unlock()
			return
		}
		sleep := time.Duration(float64(n-b.tokens) / float64(b.rate) * float64(time.Second))
		b.mu.Unlock()
		if sleep < time.Millisecond {
			sleep = time.Millisecond
		}
		if sleep > time.Second {
			sleep = time.Second
		}
		time.Sleep(sleep)
	}
}

func (b *bucket) refillLocked(now time.Time) {
	if b.last.IsZero() {
		b.last = now
		return
	}
	if b.rate <= 0 {
		b.last = now
		return
	}
	elapsed := now.Sub(b.last)
	if elapsed <= 0 {
		return
	}
	b.tokens = min(b.cap, b.tokens+elapsed.Nanoseconds()*b.rate/int64(time.Second))
	b.last = now
}
