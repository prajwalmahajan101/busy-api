package breaker

import (
	"context"
	"sync"
	"time"

	"github.com/prajwalmahajan101/busyapi/internal/errs"
)

type memoryBreaker struct {
	name string
	cfg  Config

	mu       sync.Mutex
	failures int
	state    State
	openedAt time.Time
}

func newMemoryBreaker(name string, cfg Config) *memoryBreaker {
	return &memoryBreaker{name: name, cfg: cfg}
}

// stateLocked returns the current state, transitioning OPEN→HALF_OPEN once the
// recovery window has elapsed. Caller holds mu.
func (b *memoryBreaker) stateLocked() State {
	if b.state == StateOpen && time.Since(b.openedAt) >= b.cfg.Recovery {
		b.state = StateHalfOpen
	}
	return b.state
}

func (b *memoryBreaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stateLocked()
}

// Allow reports whether a call may proceed (CLOSED or a HALF_OPEN trial). Split
// from Record so the Valkey breaker can reuse it as a warm mirror.
func (b *memoryBreaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stateLocked() != StateOpen
}

// Record feeds a call outcome into the breaker: success closes it, a
// breaker-tripping failure increments toward (or trips) OPEN, and any other
// error is ignored.
func (b *memoryBreaker) Record(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.stateLocked()
	switch {
	case err == nil:
		b.failures = 0
		b.state = StateClosed
	case errs.TripsBreaker(err):
		b.failures++
		// A failed trial in HALF_OPEN, or hitting the threshold in CLOSED, opens.
		if st == StateHalfOpen || b.failures >= b.cfg.FailThreshold {
			b.state = StateOpen
			b.openedAt = time.Now()
		}
	}
}

func (b *memoryBreaker) Call(_ context.Context, fn func() error) error {
	if !b.Allow() {
		return errs.NewServiceUnavailable(b.name)
	}
	err := fn()
	b.Record(err)
	return err
}
