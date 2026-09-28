//go:build integration

// Integration tests for the Valkey cache backend.
// Run: VALKEY_URL=redis://localhost:6379/0 go test -tags=integration ./internal/resilience/cache/
package cache

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

func TestValkeyCache_Integration(t *testing.T) {
	rdb := testClient(t)
	c := newValkeyCache("itest", rdb)
	ctx := context.Background()
	t.Cleanup(func() { rdb.Del(ctx, c.key("k"), c.key("ttl")) })

	// miss
	if _, hit, err := c.Get(ctx, "k"); err != nil || hit {
		t.Fatalf("get empty = (hit %v, err %v), want (false, nil)", hit, err)
	}

	// set/get round-trip
	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	val, hit, err := c.Get(ctx, "k")
	if err != nil || !hit || string(val) != "v" {
		t.Fatalf("get = (%q, %v, %v), want (\"v\", true, nil)", val, hit, err)
	}

	// delete
	if err := c.Delete(ctx, "k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, hit, _ := c.Get(ctx, "k"); hit {
		t.Fatal("expected miss after delete")
	}

	// TTL expiry
	if err := c.Set(ctx, "ttl", []byte("x"), 1*time.Second); err != nil {
		t.Fatalf("set ttl: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, hit, _ := c.Get(ctx, "ttl"); hit {
		t.Fatal("expected miss after TTL expiry")
	}
}

func TestValkeyCache_FailOpen(t *testing.T) {
	// Client to a dead address: every op must fail open, never return an error.
	bad := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = bad.Close() })
	c := newValkeyCache("x", bad)
	ctx := context.Background()

	if _, hit, err := c.Get(ctx, "k"); err != nil || hit {
		t.Fatalf("fail-open get = (hit %v, err %v), want (false, nil)", hit, err)
	}
	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("fail-open set err = %v, want nil", err)
	}
	if err := c.Delete(ctx, "k"); err != nil {
		t.Fatalf("fail-open delete err = %v, want nil", err)
	}
}
