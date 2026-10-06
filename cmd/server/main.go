package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prajwalmahajan101/busyapi/internal/config"
	"github.com/prajwalmahajan101/busyapi/internal/db"
	"github.com/prajwalmahajan101/busyapi/internal/errs"
	"github.com/prajwalmahajan101/busyapi/internal/items"
	"github.com/prajwalmahajan101/busyapi/internal/logging"
	"github.com/prajwalmahajan101/busyapi/internal/middleware"
	"github.com/prajwalmahajan101/busyapi/internal/resilience/cache"
	"github.com/prajwalmahajan101/busyapi/internal/response"
	"github.com/prajwalmahajan101/busyapi/internal/valkey"
)

// Response bodies for the temporary infra routes.
const (
	msgPong             = "pong"
	msgResourceNotFound = "resource not found"
)

func main() {
	logger := logging.Setup()
	if err := run(logger); err != nil {
		logger.Error("startup failed", "err", err)
		os.Exit(1)
	}
}

// run wires dependencies, builds the router, and serves. Returning an error
// keeps a single exit point and lets deferred cleanup run before the process
// exits.
func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config load: %w", err)
	}

	pool, err := initDB(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer db.Close(pool)

	// Tiered cache-aside backend for the hot read: L1 (in-process LRU) → L2
	// (Valkey, guarded by a self-healing breaker) → DB. A nil Valkey client
	// (VALKEY_URL empty) falls back to an in-memory cache. A Valkey outage is
	// absorbed by L1 (DB stays flat) and the breaker skips the dead backend
	// (no dial tax), auto-recovering when Valkey returns.
	valkey.Configure(cfg.ValkeyURL)
	rdb, err := valkey.Client("cache")
	if err != nil {
		return fmt.Errorf("valkey client: %w", err)
	}
	defer func() { _ = valkey.Close() }()
	itemCache := cache.NewTiered("items", rdb, cache.TierConfig{
		L1Enabled:            cfg.CacheL1Enabled,
		L1Max:                cfg.CacheL1Max,
		L1TTL:                time.Duration(cfg.CacheL1TTLS) * time.Second,
		BreakerFailThreshold: cfg.CacheBreakerFailThreshold,
		BreakerRecovery:      time.Duration(cfg.CacheBreakerRecoveryS) * time.Second,
		TTLJitterPct:         cfg.CacheTTLJitterPct,
	})

	// Bloom pre-filter of existing ids (cache penetration, T28). Built once at
	// boot from the items table; nil when disabled, which keeps the Get path's
	// short-circuit a no-op.
	var presence *items.Presence
	if cfg.CacheBloomEnabled {
		presence, err = items.NewPresence(context.Background(), pool, cfg.CacheBloomCapacity, cfg.CacheBloomFPRate)
		if err != nil {
			return fmt.Errorf("presence init: %w", err)
		}
	}

	r := buildRouter(cfg, logger, pool, itemCache, presence)
	return r.Run(":" + cfg.Port)
}

// initDB opens the Postgres connection pool. The caller owns closing it.
func initDB(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	pool, err := db.NewPool(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db pool init: %w", err)
	}
	return pool, nil
}

// buildRouter builds the Gin engine: it installs the middleware stack in the
// documented order via middleware.Setup, then registers every route. No
// business logic lives here. The per-IP throttle group returns at rung 5 (T30),
// when the resilience/throttle backend is re-introduced.
func buildRouter(cfg *config.Config, logger *slog.Logger, pool *pgxpool.Pool, itemCache cache.Cache, presence *items.Presence) *gin.Engine {
	r := gin.New()
	middleware.Setup(r, cfg, logger)
	registerRoutes(r, pool, itemCache, time.Duration(cfg.CacheItemTTLS)*time.Second, time.Duration(cfg.CacheNegTTLS)*time.Second, presence)
	return r
}

// registerRoutes is the single home for every HTTP route: infra routes and, for
// now, domain routes on the root engine.
func registerRoutes(r *gin.Engine, pool *pgxpool.Pool, itemCache cache.Cache, cacheTTL, negTTL time.Duration, presence *items.Presence) {
	r.GET("/ping", func(c *gin.Context) {
		response.Success(c, http.StatusOK, msgPong, nil)
	})

	// Temporary route proving the error envelope; drops once real routes land.
	r.GET("/error", func(c *gin.Context) {
		response.Error(c, errs.NewNotFound(msgResourceNotFound))
	})

	items.NewHandler(items.NewService(pool, itemCache, cacheTTL, negTTL, presence)).RegisterRoutes(r)
}
