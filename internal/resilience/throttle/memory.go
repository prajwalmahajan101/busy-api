package throttle

import (
	"context"
	"sync"
	"time"
)

type memoryThrottle struct {
	mu   sync.Mutex
	hits map[string][]int64 // id -> ascending unix-nano timestamps in window
}

func newMemoryThrottle() *memoryThrottle {
	return &memoryThrottle{hits: map[string][]int64{}}
}

func (t *memoryThrottle) Check(_ context.Context, id string, limit int, window time.Duration) (Result, error) {
	now := time.Now()
	winStart := now.Add(-window).UnixNano()

	t.mu.Lock()
	defer t.mu.Unlock()

	// Drop timestamps older than the window, then record this hit.
	kept := t.hits[id][:0]
	for _, ts := range t.hits[id] {
		if ts >= winStart {
			kept = append(kept, ts)
		}
	}
	kept = append(kept, now.UnixNano())
	t.hits[id] = kept
	count := len(kept)
	return Result{
		Allowed:   count <= limit,
		Limit:     limit,
		Remaining: max(0, limit-count),
		Reset:     now.Add(window).Unix(),
	}, nil
}
