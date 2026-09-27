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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prajwalmahajan101/busyapi/internal/config"
	"github.com/prajwalmahajan101/busyapi/internal/db"
	storedb "github.com/prajwalmahajan101/busyapi/internal/store/db"
)

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
	svc := NewService(pool)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := svc.Create(ctx, []byte(`{"n":`+strconv.Itoa(i)+`}`)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// Page 1 of 2: two items, total 3, more to come.
	page1, total, err := svc.List(ctx, 1, 2)
	if err != nil {
		t.Fatalf("list page1: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1))
	}

	// Page 2: remaining one item.
	page2, _, err := svc.List(ctx, 2, 2)
	if err != nil {
		t.Fatalf("list page2: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page2 len = %d, want 1", len(page2))
	}

	// Soft-delete first item → excluded from list and count.
	if err := svc.SoftDelete(ctx, page1[0].ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	_, total, err = svc.List(ctx, 1, 10)
	if err != nil {
		t.Fatalf("list after soft-delete: %v", err)
	}
	if total != 2 {
		t.Fatalf("total after soft-delete = %d, want 2", total)
	}

	// Soft-deleted row is not retrievable.
	if _, err := svc.Get(ctx, page1[0].ID); err == nil {
		t.Fatal("expected not-found for soft-deleted item")
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
