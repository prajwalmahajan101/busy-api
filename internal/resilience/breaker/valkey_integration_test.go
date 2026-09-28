//go:build integration

// Integration tests for the Valkey circuit breaker.
// Run: VALKEY_URL=redis://localhost:6379/0 go test -tags=integration ./internal/resilience/breaker/
package breaker

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/prajwalmahajan101/busyapi/internal/errs"
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

func TestValkeyBreaker_OpensAtThreshold(t *testing.T) {
	rdb := testClient(t)
	cfg := Config{FailThreshold: 3, Recovery: time.Minute}
	b := newValkeyBreaker("itest", cfg, rdb)
	ctx := context.Background()

	// clean slate
	rdb.Del(ctx, b.failKey, b.openKey, b.halfOpenKey, b.trialKey)
	t.Cleanup(func() { rdb.Del(ctx, b.failKey, b.openKey, b.halfOpenKey, b.trialKey) })

	trip := func() error { return errs.NewTransient("boom") }
	for i := 0; i < 3; i++ {
		_ = b.Call(ctx, trip)
	}
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want open", b.State())
	}

	// OPEN rejects without calling fn.
	called := false
	err := b.Call(ctx, func() error {
		called = true
		return nil
	})
	if called {
		t.Fatal("fn called while breaker open")
	}
	if err == nil {
		t.Fatal("expected ServiceUnavailable, got nil")
	}
}

func TestValkeyBreaker_FailOpenToMirror(t *testing.T) {
	// Valkey unreachable: Call must degrade to the in-memory mirror and still run
	// fn (closed mirror admits it), never blocking traffic.
	bad := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = bad.Close() })
	b := newValkeyBreaker("x", Config{FailThreshold: 3, Recovery: time.Minute}, bad)
	ctx := context.Background()

	called := false
	err := b.Call(ctx, func() error {
		called = true
		return nil
	})
	if !called {
		t.Fatal("fn not called; mirror should admit when Valkey is down")
	}
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}
