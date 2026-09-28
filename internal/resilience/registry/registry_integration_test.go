package registry

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/prajwalmahajan101/busyapi/internal/config"
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

func TestResilient_OpensBreakerAfterThreshold(t *testing.T) {
	rdb := testClient(t)
	// RetryMax=1 → one fn call per Resilient, so each failure is one breaker count.
	cfg := &config.Config{RetryMax: 1, RetryBaseMS: 1, BreakerFailThreshold: 3, BreakerRecoveryS: 60}
	reg := New(cfg, rdb)
	ctx := context.Background()
	const svc = "itest-svc"

	// clean slate
	rdb.Del(ctx,
		"breaker:"+svc+":failures", "breaker:"+svc+":open",
		"breaker:"+svc+":halfopen", "breaker:"+svc+":trial")
	t.Cleanup(func() {
		rdb.Del(ctx,
			"breaker:"+svc+":failures", "breaker:"+svc+":open",
			"breaker:"+svc+":halfopen", "breaker:"+svc+":trial")
	})

	flaky := func() error { return errs.NewTransient("down") }
	for i := 0; i < cfg.BreakerFailThreshold; i++ {
		_ = reg.Resilient(ctx, svc, flaky)
	}

	// Breaker now OPEN: fn must not run, error is ServiceUnavailable.
	called := false
	err := reg.Resilient(ctx, svc, func() error {
		called = true
		return nil
	})
	if called {
		t.Fatal("fn called while breaker open")
	}
	var ae *errs.AppError
	if !errors.As(err, &ae) || ae.Code != "service_unavailable" {
		t.Fatalf("err = %v, want service_unavailable", err)
	}
}
