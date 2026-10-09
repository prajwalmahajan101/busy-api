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
	"sync"
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
	svc := NewService(pool, nil, memCache(), time.Minute, time.Minute, nil)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := svc.Create(ctx, []byte(`{"n":`+strconv.Itoa(i)+`}`)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// Assertions are on the actual returned rows (exact via ListItems).

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

// TestListVersionInvalidation_Integration proves the list cache is invalidated on
// writes: re-listing the SAME (page,size) after a Create/SoftDelete must reflect
// the change, not a stale cached page. Uses the in-memory cache (atomic-fallback
// version path, single-instance). The pre-fix code (no version bump) fails the
// post-write assertions.
func TestListVersionInvalidation_Integration(t *testing.T) {
	pool := setupPool(t)
	svc := NewService(pool, nil, memCache(), time.Minute, time.Minute, nil)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := svc.Create(ctx, []byte(`{"n":`+strconv.Itoa(i)+`}`)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// Populate the cache for page 1.
	page, _, err := svc.List(ctx, 1, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("len = %d, want 2", len(page))
	}

	// Create a third item, then re-list the SAME page: must show 3, not a stale 2.
	if _, err := svc.Create(ctx, []byte(`{"n":2}`)); err != nil {
		t.Fatalf("create 3rd: %v", err)
	}
	page, _, err = svc.List(ctx, 1, 10)
	if err != nil {
		t.Fatalf("list after create: %v", err)
	}
	if len(page) != 3 {
		t.Fatalf("stale list after create: len = %d, want 3", len(page))
	}

	// Soft-delete one, re-list the same page: must drop to 2.
	if err := svc.SoftDelete(ctx, page[0].ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	page, _, err = svc.List(ctx, 1, 10)
	if err != nil {
		t.Fatalf("list after soft-delete: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("stale list after soft-delete: len = %d, want 2", len(page))
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
	created, err := NewService(pool, nil, memCache(), time.Minute, time.Minute, nil).Create(ctx, []byte(`{"k":1}`))
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	// Cache backend wired to a dead port: Get and Set both fail and must fall open.
	down := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:6390", // nothing listening
		DialTimeout: 200 * time.Millisecond,
	})
	t.Cleanup(func() { _ = down.Close() })
	svc := NewService(pool, nil, cache.NewProvider(down).Get("items"), time.Minute, time.Minute, nil)

	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get with Valkey down must succeed from DB, got err: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("Get returned id %d, want %d", got.ID, created.ID)
	}
}

// barrierCache is a cache.Cache for the singleflight test: every Get blocks until
// all N callers have arrived, then returns a miss — forcing all N into the loader
// at once (the exact concurrent-miss condition the stampede needs). It counts Set
// calls, which in Service.Get follow a successful DB read, so setCount == DB loads.
type barrierCache struct {
	n       int
	mu      sync.Mutex
	arrived int
	release chan struct{}
	sets    int
}

func newBarrierCache(n int) *barrierCache { return &barrierCache{n: n, release: make(chan struct{})} }

func (c *barrierCache) Get(_ context.Context, _ string) ([]byte, bool, error) {
	c.mu.Lock()
	c.arrived++
	if c.arrived == c.n {
		close(c.release) // last arrival frees everyone — now all miss together
	}
	c.mu.Unlock()
	<-c.release
	return nil, false, nil // always a miss
}

func (c *barrierCache) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	c.mu.Lock()
	c.sets++
	c.mu.Unlock()
	return nil
}

func (c *barrierCache) Delete(_ context.Context, _ string) error { return nil }

// TestSingleflight_CollapsesConcurrentMisses proves T26: N concurrent misses on
// the same key collapse to a single DB load. The barrier guarantees the concurrent
// condition deterministically (not by timing luck); without singleflight each of
// the N misses would Set once (N DB loads) — with it, exactly one.
func TestSingleflight_CollapsesConcurrentMisses(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()

	created, err := NewService(pool, nil, memCache(), time.Minute, time.Minute, nil).Create(ctx, []byte(`{"hot":1}`))
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	const n = 50
	bc := newBarrierCache(n)
	svc := NewService(pool, nil, bc, time.Minute, time.Minute, nil)

	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			if _, gErr := svc.Get(ctx, created.ID); gErr != nil {
				t.Errorf("concurrent Get: %v", gErr)
			}
		}()
	}
	wg.Wait()

	bc.mu.Lock()
	sets := bc.sets
	bc.mu.Unlock()
	if sets != 1 {
		t.Fatalf("cache Set called %d times; want 1 — singleflight must collapse %d concurrent misses to one DB load", sets, n)
	}
}

// Cache penetration (T28): the bloom pre-filter must 404 a definitely-absent id
// without touching the DB, while never false-negatively 404ing a real item.
func TestBloomShortCircuit_Integration(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()

	seed := NewService(pool, nil, memCache(), time.Minute, time.Minute, nil)
	real, err := seed.Create(ctx, []byte(`{"real":1}`))
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	presence, err := NewPresence(ctx, pool, 10000, 0.01)
	if err != nil {
		t.Fatalf("new presence: %v", err)
	}
	svc := NewService(pool, nil, memCache(), time.Minute, time.Minute, presence)

	// A real id must still resolve — the bloom has no false negatives.
	if _, err := svc.Get(ctx, real.ID); err != nil {
		t.Fatalf("real id %d short-circuited (false negative): %v", real.ID, err)
	}

	// An out-of-range absent id must 404 (bloom says definitely-absent).
	if _, err := svc.Get(ctx, 1_000_000_000); err == nil {
		t.Fatal("absent id resolved — penetration not blocked")
	}
}

// Negative cache (T28): a confirmed-absent id (reached with the bloom disabled,
// simulating a false positive that falls through) is tombstoned, so a repeat is
// served from cache. We assert the tombstone lands in the cache after one miss.
func TestNegativeCache_Integration(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()

	c := memCache()
	// presence nil → bloom disabled, so the absent id reaches the DB once and the
	// loader writes the tombstone (the false-positive path, forced deterministically).
	svc := NewService(pool, nil, c, time.Minute, time.Minute, nil)

	const absent = int64(1_234_567)
	if _, err := svc.Get(ctx, absent); err == nil {
		t.Fatal("absent id resolved")
	}

	b, hit, _ := c.Get(ctx, itemCacheKey(absent))
	if !hit {
		t.Fatal("no tombstone written after a confirmed-absent read — repeats would re-hit the DB")
	}
	if string(b) != string(tombstone) {
		t.Fatalf("cached value %q is not the tombstone — negative cache corrupted", b)
	}

	// A second read still 404s (served from the tombstone).
	if _, err := svc.Get(ctx, absent); err == nil {
		t.Fatal("second absent read resolved")
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
