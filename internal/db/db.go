// Package db builds and manages the Postgres connection pool. Every query runs
// under a per-call timeout so a slow database never stalls a goroutine
// indefinitely (PRD §6 non-negotiable).
package db

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prajwalmahajan101/busyapi/internal/config"
)

// queryTimeout is the per-call query deadline, set once by NewPool. A single
// process holds a single app pool, so a package var is enough.
// ponytail: package-level timeout, set once at NewPool; thread through a struct
// only if a second pool with a different budget is ever needed.
var queryTimeout = 2 * time.Second

// NewPool builds a pgxpool sized at DB_MAX_CONNS (default 4×NumCPU), with the
// pool connect timeout from DB_CONN_TIMEOUT_MS, and pings once to fail fast.
func NewPool(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: parse DATABASE_URL: %w", err)
	}

	maxConns := cfg.DBMaxConns
	if maxConns <= 0 {
		maxConns = 4 * runtime.NumCPU()
	}
	poolCfg.MaxConns = int32(maxConns)
	poolCfg.ConnConfig.ConnectTimeout = time.Duration(cfg.DBConnTimeoutMS) * time.Millisecond

	queryTimeout = time.Duration(cfg.DBQueryTimeoutMS) * time.Millisecond

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, poolCfg.ConnConfig.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// WithQueryTimeout derives a child context bounded by DB_QUERY_TIMEOUT_MS.
// Every DB call in the store layer must scope its context through this.
// Caller MUST call cancel.
func WithQueryTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, queryTimeout)
}

// Atomic runs fn inside a transaction under the query timeout. It commits on
// nil, rolls back on error. The rollback error is ignored when fn already
// failed — fn's error is the meaningful one.
func Atomic(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	txCtx, cancel := WithQueryTimeout(ctx)
	defer cancel()

	tx, err := pool.Begin(txCtx)
	if err != nil {
		return fmt.Errorf("db: begin tx: %w", err)
	}

	if err := fn(tx); err != nil {
		_ = tx.Rollback(txCtx)
		return err
	}

	if err := tx.Commit(txCtx); err != nil {
		_ = tx.Rollback(txCtx)
		return fmt.Errorf("db: commit: %w", err)
	}
	return nil
}

// Close releases all pool connections. Safe to call on a nil pool.
func Close(pool *pgxpool.Pool) {
	if pool != nil {
		pool.Close()
	}
}
