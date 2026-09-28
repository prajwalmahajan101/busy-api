//go:build integration

// Integration tests for the Valkey sliding-window throttle.
// Run: VALKEY_URL=redis://localhost:6379/0 go test -tags=integration ./internal/resilience/throttle/
package throttle

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func testClient(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("VALKEY_URL")
	if url == "" {
		t.Skip("VALKEY_URL not set")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse VALKEY_URL: %v", err)
	}
	c := redis.NewClient(opt)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestValkeyThrottle_Integration(t *testing.T) {
	rdb := testClient(t)
	th := newValkeyThrottle(rdb)
	ctx := context.Background()
	const id, limit = "itest:ep:/items", 3
	window := time.Minute
	t.Cleanup(func() { rdb.Del(ctx, "throttle:"+id) })
	rdb.Del(ctx, "throttle:"+id)

	for i := 1; i <= limit; i++ {
		r, err := th.Check(ctx, id, limit, window)
		if err != nil || !r.Allowed {
			t.Fatalf("call %d: allowed=%v err=%v, want allowed", i, r.Allowed, err)
		}
		if r.Remaining != limit-i {
			t.Fatalf("call %d: remaining = %d, want %d", i, r.Remaining, limit-i)
		}
	}
	// Over the limit → rejected, headers still populated.
	r, err := th.Check(ctx, id, limit, window)
	if err != nil {
		t.Fatalf("over-limit err: %v", err)
	}
	if r.Allowed {
		t.Fatal("over-limit call allowed, want rejected")
	}
	if r.Limit != limit || r.Remaining != 0 {
		t.Fatalf("over-limit result = %+v, want Limit=%d Remaining=0", r, limit)
	}
}

func TestValkeyThrottle_FailOpenToMemory(t *testing.T) {
	// Valkey unreachable → fall back to the in-memory limiter, which still
	// enforces the limit rather than erroring.
	bad := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = bad.Close() })
	th := newValkeyThrottle(bad)
	ctx := context.Background()
	const id, limit = "x", 2
	window := time.Minute

	for i := 1; i <= limit; i++ {
		if r, err := th.Check(ctx, id, limit, window); err != nil || !r.Allowed {
			t.Fatalf("call %d: allowed=%v err=%v, want allowed via memory fallback", i, r.Allowed, err)
		}
	}
	if r, err := th.Check(ctx, id, limit, window); err != nil || r.Allowed {
		t.Fatalf("over-limit fallback: allowed=%v err=%v, want rejected", r.Allowed, err)
	}
}
