package breaker

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/prajwalmahajan101/busyapi/internal/errs"
)

// valkeyBreaker is the cross-replica breaker. State lives in per-dep keys shared
// by all instances; a warm in-memory mirror (mem) takes over whenever Valkey is
// unreachable so an outage degrades to local protection instead of fail-open.
type valkeyBreaker struct {
	name string
	cfg  Config
	rdb  *redis.Client
	mem  *memoryBreaker

	failKey     string
	openKey     string
	halfOpenKey string
	trialKey    string
}

func newValkeyBreaker(name string, cfg Config, rdb *redis.Client) *valkeyBreaker {
	return &valkeyBreaker{
		name:        name,
		cfg:         cfg,
		rdb:         rdb,
		mem:         newMemoryBreaker(name, cfg),
		failKey:     fmt.Sprintf("breaker:%s:failures", name),
		openKey:     fmt.Sprintf("breaker:%s:open", name),
		halfOpenKey: fmt.Sprintf("breaker:%s:halfopen", name),
		trialKey:    fmt.Sprintf("breaker:%s:trial", name),
	}
}

func (b *valkeyBreaker) State() State {
	ctx := context.Background()
	open, err := b.rdb.Exists(ctx, b.openKey).Result()
	if err != nil {
		return b.mem.State() // Valkey down: report the mirror's view
	}
	if open > 0 {
		return StateOpen
	}
	if ho, err := b.rdb.Exists(ctx, b.halfOpenKey).Result(); err == nil && ho > 0 {
		return StateHalfOpen
	}
	return StateClosed
}

func (b *valkeyBreaker) Call(ctx context.Context, fn func() error) error {
	admit, ok := b.allow(ctx)
	if !ok {
		// Valkey unreachable: degrade to the warm in-memory mirror.
		return b.mem.Call(ctx, fn)
	}
	if !admit {
		return errs.NewServiceUnavailable(b.name)
	}

	callErr := fn()
	b.record(ctx, callErr)
	b.mem.Record(callErr) // keep the mirror warm for a future Valkey outage
	return callErr
}

// allow returns (admit, ok). ok is false when Valkey errors — the caller then
// falls back to the mirror. Three distributed states: open present → deny; open
// gone but halfopen present → recovery, admit only the first HalfOpenTrials
// probes fleet-wide; neither present → fully closed.
func (b *valkeyBreaker) allow(ctx context.Context) (bool, bool) {
	open, err := b.rdb.Exists(ctx, b.openKey).Result()
	if err != nil {
		return false, false
	}
	if open > 0 {
		return false, true
	}

	ho, err := b.rdb.Exists(ctx, b.halfOpenKey).Result()
	if err != nil {
		return false, false
	}
	if ho == 0 {
		return true, true // fully closed
	}

	// Recovery phase: admit the first N trials, deny the rest (anti-stampede).
	n, err := b.rdb.Incr(ctx, b.trialKey).Result()
	if err != nil {
		return false, false
	}
	b.rdb.Expire(ctx, b.trialKey, b.recovery())
	return int(n) <= b.cfg.halfOpenTrials(), true
}

// record folds a call outcome into the shared counters. Best-effort: a Valkey
// error here just means the mirror carries the state until Valkey returns.
func (b *valkeyBreaker) record(ctx context.Context, callErr error) {
	switch {
	case callErr == nil:
		// Success closes the breaker fleet-wide: clear every key.
		b.rdb.Del(ctx, b.failKey, b.openKey, b.halfOpenKey, b.trialKey)
	case errs.TripsBreaker(callErr):
		n, err := b.rdb.Incr(ctx, b.failKey).Result()
		if err != nil {
			return
		}
		b.rdb.Expire(ctx, b.failKey, b.recovery())
		if int(n) >= b.cfg.FailThreshold {
			b.open(ctx)
		}
	}
	// Non-tripping errors leave the breaker untouched.
}

// open trips the breaker: the open flag lasts one recovery window; the halfopen
// marker outlives it so "open gone, halfopen present" unambiguously means the
// recovery phase, while "both absent" means fully closed.
func (b *valkeyBreaker) open(ctx context.Context) {
	b.rdb.Set(ctx, b.openKey, "1", b.recovery())
	b.rdb.Set(ctx, b.halfOpenKey, "1", 2*b.recovery())
	b.rdb.Del(ctx, b.failKey, b.trialKey)
}

func (b *valkeyBreaker) recovery() time.Duration { return b.cfg.Recovery }
