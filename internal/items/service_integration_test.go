//go:build integration

// Integration tests for the items module against a real Postgres.
// Run: go test -tags=integration ./internal/items/
// Requires DATABASE_URL pointing at the project database (busyapi) with the
// 0001_base migration applied.
package items

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/prajwalmahajan101/busyapi/internal/config"
	"github.com/prajwalmahajan101/busyapi/internal/db"
	"github.com/prajwalmahajan101/busyapi/internal/resilience/cache"
	storedb "github.com/prajwalmahajan101/busyapi/internal/store/db"
)

// memCache returns an in-memory cache backend — the fallback the Provider hands
// out when there is no Valkey client. Used by CRUD/rollback tests that are not
// about the cache backend.
func memCache() cache.Cache { return cache.NewProvider(nil).Get("items") }

// setupPool builds a pool from DATABASE_URL and truncates items for a clean
// slate. Skips when DATABASE_URL is unset so `go test` without the tag/env is a
// no-op rather than a failure.
func setupPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	if cfg.DatabaseURL == "" {
		t.Skip("DATABASE_URL not set")
	}

	pool, err := db.NewPool(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(func() { db.Close(pool) })

	if _, err := pool.Exec(context.Background(), "TRUNCATE items RESTART IDENTITY"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

func countItems(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	n, err := storedb.New(pool).CountItems(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestItemsCRUD_Integration(t *testing.T) {
	pool := setupPool(t)
	svc := NewService(pool, memCache(), time.Minute)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := svc.Create(ctx, []byte(`{"n":`+strconv.Itoa(i)+`}`)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// Assertions are on the actual returned rows (exact via ListItems), not the
	// `total` — which is now a reltuples ESTIMATE (rung 2) and does not reflect
	// uncommitted stats or the is_active filter.

	// Page 1 of 2: two of the three rows.
	page1, _, err := svc.List(ctx, 1, 2)
	if err != nil {
		t.Fatalf("list page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1))
	}

	// Page 2: the remaining row.
	page2, _, err := svc.List(ctx, 2, 2)
	if err != nil {
		t.Fatalf("list page2: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page2 len = %d, want 1", len(page2))
	}

	// Soft-delete the first item → excluded from the active list.
	if delErr := svc.SoftDelete(ctx, page1[0].ID); delErr != nil {
		t.Fatalf("soft delete: %v", delErr)
	}
	active, _, err := svc.List(ctx, 1, 10)
	if err != nil {
		t.Fatalf("list after soft-delete: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("active rows after soft-delete = %d, want 2", len(active))
	}

	// Soft-deleted row is not retrievable (and the cache entry was invalidated).
	if _, err := svc.Get(ctx, page1[0].ID); err == nil {
		t.Fatal("expected not-found for soft-deleted item")
	}
}

// TestCacheFailOpen_Integration proves F-6 / T20: with the cache backend pointed
// at a DOWN Valkey, every cache op fails open to a miss and the read is served
// from Postgres — never a 5xx. A cache outage degrades latency, never
// availability.
func TestCacheFailOpen_Integration(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()

	// Seed one row (via an in-memory-cached service, unrelated to the assertion).
	created, err := NewService(pool, memCache(), time.Minute).Create(ctx, []byte(`{"k":1}`))
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	// Cache backend wired to a dead port: Get and Set both fail and must fall open.
	down := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:6390", // nothing listening
		DialTimeout: 200 * time.Millisecond,
	})
	t.Cleanup(func() { _ = down.Close() })
	svc := NewService(pool, cache.NewProvider(down).Get("items"), time.Minute)

	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get with Valkey down must succeed from DB, got err: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("Get returned id %d, want %d", got.ID, created.ID)
	}
}

func TestAtomic_Rollback_Integration(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()

	before := countItems(t, pool)
	injErr := errors.New("injected failure")

	err := db.Atomic(ctx, pool, func(tx pgx.Tx) error {
		q := storedb.New(tx)
		if _, e := q.CreateItem(ctx, []byte(`{"x":1}`)); e != nil {
			return e
		}
		return injErr // force rollback after a successful insert
	})
	if !errors.Is(err, injErr) {
		t.Fatalf("Atomic err = %v, want injected failure", err)
	}

	if after := countItems(t, pool); after != before {
		t.Fatalf("rollback failed: count %d -> %d", before, after)
	}
}
