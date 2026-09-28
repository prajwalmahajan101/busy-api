// Package registry wires the resilience primitives into one call: Resilient runs
// a function under a named circuit breaker wrapping retry-with-backoff —
// breaker(retry(fn)) — using per-service config derived from the app Config.
package registry

import (
	"context"
	"time"

	"github.com/prajwalmahajan101/busyapi/internal/config"
	"github.com/prajwalmahajan101/busyapi/internal/resilience/breaker"
	"github.com/prajwalmahajan101/busyapi/internal/resilience/retry"
	"github.com/redis/go-redis/v9"
)

// Registry hands out resilient call wrappers keyed by service name. A non-nil
// rdb backs the breakers with Valkey (+ in-memory mirror); nil is pure
// in-memory.
type Registry struct {
	breakers  *breaker.Provider
	retryOpts retry.Options
}

func New(cfg *config.Config, rdb *redis.Client) *Registry {
	base := time.Duration(cfg.RetryBaseMS) * time.Millisecond
	return &Registry{
		breakers: breaker.NewProvider(breaker.Config{
			FailThreshold: cfg.BreakerFailThreshold,
			Recovery:      time.Duration(cfg.BreakerRecoveryS) * time.Second,
		}, rdb),
		retryOpts: retry.Options{
			MaxAttempts: cfg.RetryMax,
			Base:        base,
			Max:         base * 10, // cap backoff at 10x base
		},
	}
}

// Resilient runs fn as breaker(retry(fn)) under the named service. The breaker
// rejects fast with ServiceUnavailable when OPEN; otherwise retry re-runs fn on
// transient errors within budget and the final outcome feeds the breaker.
func (r *Registry) Resilient(ctx context.Context, name string, fn func() error) error {
	b := r.breakers.Get(name)
	return b.Call(ctx, func() error {
		return retry.Do(ctx, fn, r.retryOpts)
	})
}
