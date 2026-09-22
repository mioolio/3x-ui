package throttle

import (
	"testing"
	"time"
)

func TestNewBucketUnlimitedIsNil(t *testing.T) {
	if b := newBucket(0); b != nil {
		t.Fatal("zero rate returned a bucket")
	}
}

func TestBucketWaitConsumesTokensAtRate(t *testing.T) {
	// 8 Kbps = 1000 bytes/s; capacity floor is 64 KiB. Drain the whole burst
	// first, then one more second's worth of bytes must wait for refill.
	b := newBucket(KbpsToBytesPerSec(8))
	b.wait(minBucketCapacity)
	start := time.Now()
	b.wait(1000)
	elapsed := time.Since(start)
	if elapsed < 800*time.Millisecond {
		t.Fatalf("1000 bytes at 1 KB/s took %v, expected >= 800ms", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("1000 bytes at 1 KB/s took %v, refill stalled", elapsed)
	}
}

func TestBucketSetRateToZeroIsUnlimited(t *testing.T) {
	b := newBucket(KbpsToBytesPerSec(8))
	b.wait(2000)
	b.setRate(0)
	start := time.Now()
	b.wait(64 * 1024)
	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("unlimited bucket blocked for %v", time.Since(start))
	}
}

func TestBucketSetRateCarriesTokens(t *testing.T) {
	b := newBucket(KbpsToBytesPerSec(1000))
	// Drain the burst.
	b.wait(250 * 1000)
	b.setRate(KbpsToBytesPerSec(1000))
	// After draining, a full-capacity wait must still block (tokens were not
	// resurrected by the retune).
	start := time.Now()
	b.wait(250 * 1000)
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("retune resurrected drained tokens")
	}
}
