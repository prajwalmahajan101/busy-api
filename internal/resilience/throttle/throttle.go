// Package throttle is a sliding-window rate limiter. Valkey backs it across
// replicas with an in-memory fallback when Valkey is unreachable. Callers build
// a scoped id with Key, then Check it against a limit/window.
package throttle

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Result is one throttle decision. It maps directly onto the X-RateLimit-*
// headers via middleware.SetRateLimitResult.
type Result struct {
	Allowed   bool
	Limit     int
	Remaining int
	Reset     int64 // unix seconds when the window frees up
}

// Throttler decides whether a scoped id may proceed under limit/window.
type Throttler interface {
	Check(ctx context.Context, id string, limit int, window time.Duration) (Result, error)
}

// Scope namespaces a throttle key so endpoint / IP / user-tier / burst / global
// limits never collide on a shared Valkey.
type Scope string

const (
	ScopeEndpoint Scope = "ep"
	ScopeIP       Scope = "ip"
	ScopeUser     Scope = "user"
	ScopeBurst    Scope = "burst"
	ScopeGlobal   Scope = "global"
)

// Key builds a throttle id: "<scope>:<part>:<part>".
func Key(scope Scope, parts ...string) string {
	if len(parts) == 0 {
		return string(scope)
	}
	return string(scope) + ":" + strings.Join(parts, ":")
}

// ParseRate parses "100/min" into (100, time.Minute). Units: s/sec/second,
// m/min/minute, h/hour.
func ParseRate(s string) (int, time.Duration, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("throttle: bad rate %q, want n/unit", s)
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || n <= 0 {
		return 0, 0, fmt.Errorf("throttle: bad limit in %q", s)
	}
	var w time.Duration
	switch strings.ToLower(strings.TrimSpace(parts[1])) {
	case "s", "sec", "seconds":
		w = time.Second
	case "m", "min", "minute":
		w = time.Minute
	case "h", "hour":
		w = time.Hour
	default:
		return 0, 0, fmt.Errorf("throttle: bad unit in %q", s)
	}
	return n, w, nil
}

// New returns a Valkey-backed throttler (with in-memory fallback) when rdb is
// non-nil, otherwise a pure in-memory one.
func New(rdb *redis.Client) Throttler {
	if rdb != nil {
		return newValkeyThrottle(rdb)
	}
	return newMemoryThrottle()
}
