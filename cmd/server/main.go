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

	// Cache-aside backend for the hot read. A nil Valkey client (VALKEY_URL
	// empty or unreachable at build time) makes the provider hand out an
	// in-memory cache; Valkey errors at runtime fail open as misses.
	valkey.Configure(cfg.ValkeyURL)
	rdb, err := valkey.Client("cache")
	if err != nil {
		return fmt.Errorf("valkey client: %w", err)
	}
	defer func() { _ = valkey.Close() }()
	itemCache := cache.NewProvider(rdb).Get("items")

	r := buildRouter(cfg, logger, pool, itemCache)
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
func buildRouter(cfg *config.Config, logger *slog.Logger, pool *pgxpool.Pool, itemCache cache.Cache) *gin.Engine {
	r := gin.New()
	middleware.Setup(r, cfg, logger)
	registerRoutes(r, pool, itemCache, time.Duration(cfg.CacheItemTTLS)*time.Second)
	return r
}

// registerRoutes is the single home for every HTTP route: infra routes and, for
// now, domain routes on the root engine.
func registerRoutes(r *gin.Engine, pool *pgxpool.Pool, itemCache cache.Cache, cacheTTL time.Duration) {
	r.GET("/ping", func(c *gin.Context) {
		response.Success(c, http.StatusOK, msgPong, nil)
	})

	// Temporary route proving the error envelope; drops once real routes land.
	r.GET("/error", func(c *gin.Context) {
		response.Error(c, errs.NewNotFound(msgResourceNotFound))
	})

	items.NewHandler(items.NewService(pool, itemCache, cacheTTL)).RegisterRoutes(r)
}
