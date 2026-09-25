package stats

import (
	"math"
	"sync"
	"testing"
)

func TestCounterAddSaturatesWithoutWrapping(t *testing.T) {
	counter := &Counter{}
	counter.Set(math.MaxInt64 - 3)
	if got := counter.Add(10); got != math.MaxInt64 || counter.Value() != math.MaxInt64 {
		t.Fatalf("positive counter wrapped: value %d", counter.Value())
	}
	counter.Set(math.MinInt64 + 3)
	if got := counter.Add(-10); got != math.MinInt64 || counter.Value() != math.MinInt64 {
		t.Fatalf("negative counter wrapped: value %d", counter.Value())
	}
}

func TestCounterAddConcurrentSaturates(t *testing.T) {
	counter := &Counter{}
	counter.Set(math.MaxInt64 - 100)
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() { counter.Add(10) })
	}
	workers.Wait()
	if got := counter.Value(); got != math.MaxInt64 {
		t.Fatalf("concurrent charged counter wrapped: %d", got)
	}
}
