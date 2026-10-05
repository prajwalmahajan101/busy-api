// Package breaker is a circuit breaker over a dependency. CLOSED passes calls
// and counts breaker-tripping failures; at the threshold it trips OPEN and
// rejects fast with a ServiceUnavailable error without calling fn; after the
// recovery window it admits a trial (HALF_OPEN) that either closes or re-opens it
// (NFR-R1/R5).
//
// Hand-rolled, consecutive-count model per ADR-0003 (no breaker library). Only
// the in-memory implementation is re-introduced at rung 4 — the cache breaker
// must be in-process, since a Valkey-backed breaker is useless when Valkey is the
// thing that is down. The Valkey (cross-replica) breaker variant lands with the
// outbound-resilience work (T64).
package breaker

import (
	"context"
	"time"
)

type State int

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}

// Breaker guards a named dependency.
type Breaker interface {
	// Call runs fn unless the breaker is OPEN, in which case it returns a
	// ServiceUnavailable error without calling fn. Only breaker-tripping errors
	// count toward opening.
	Call(ctx context.Context, fn func() error) error
	State() State
}

// Config tunes a breaker.
type Config struct {
	FailThreshold int
	Recovery      time.Duration
}

// NewMemory returns an in-process breaker for name.
func NewMemory(name string, cfg Config) Breaker {
	return newMemoryBreaker(name, cfg)
}
