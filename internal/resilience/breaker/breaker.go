// Package breaker is a circuit breaker over outbound calls. CLOSED passes calls
// and counts breaker-tripping failures; at the threshold it trips OPEN and
// rejects fast with a ServiceUnavailable error; after the recovery window it
// admits a limited number of trials (HALF_OPEN) that either close or re-open it
// (R12, NFR-R1/R2).
//
// Hand-rolled, consecutive-count model per ADR-0003 (no breaker library). Two
// behaviours are borrowed from the document-service reference: on a Valkey
// outage the distributed breaker degrades to a warm in-memory mirror rather than
// fail-open, and HALF_OPEN admits only HalfOpenTrials probes fleet-wide to avoid
// a recovery stampede.
package breaker

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
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

// Config tunes every breaker the provider builds.
type Config struct {
	FailThreshold  int
	Recovery       time.Duration
	HalfOpenTrials int // fleet-wide probes admitted during recovery; <1 means 1
}

func (c Config) halfOpenTrials() int {
	if c.HalfOpenTrials < 1 {
		return 1
	}
	return c.HalfOpenTrials
}

// Provider builds and caches breakers by name. A non-nil rdb selects the Valkey
// (cross-replica) implementation with an in-memory mirror; nil selects the pure
// in-memory one.
type Provider struct {
	cfg      Config
	rdb      *redis.Client
	mu       sync.Mutex
	breakers map[string]Breaker
}

func NewProvider(cfg Config, rdb *redis.Client) *Provider {
	return &Provider{cfg: cfg, rdb: rdb, breakers: map[string]Breaker{}}
}

// Get returns the breaker for name, cached across calls.
func (p *Provider) Get(name string) Breaker {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b, ok := p.breakers[name]; ok {
		return b
	}
	var b Breaker
	if p.rdb != nil {
		b = newValkeyBreaker(name, p.cfg, p.rdb)
	} else {
		b = newMemoryBreaker(name, p.cfg)
	}
	p.breakers[name] = b
	return b
}
