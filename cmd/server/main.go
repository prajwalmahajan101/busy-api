package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/prajwalmahajan101/busyapi/internal/config"
	"github.com/prajwalmahajan101/busyapi/internal/db"
	"github.com/prajwalmahajan101/busyapi/internal/errs"
	"github.com/prajwalmahajan101/busyapi/internal/items"
	"github.com/prajwalmahajan101/busyapi/internal/logging"
	"github.com/prajwalmahajan101/busyapi/internal/middleware"
	"github.com/prajwalmahajan101/busyapi/internal/resilience/throttle"
	"github.com/prajwalmahajan101/busyapi/internal/response"
	"github.com/prajwalmahajan101/busyapi/internal/valkey"
)

// defaultRate is the per-IP request budget applied to domain routes.
const defaultRate = "100/min"

// msgResourceNotFound is the body for the catch-all no-route handler.
const msgResourceNotFound = "resource not found"

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

	pool, err := db.NewPool(context.Background(), cfg)
	if err != nil {
		return fmt.Errorf("db pool init: %w", err)
	}
	defer db.Close(pool)

	valkey.Configure(cfg.ValkeyURL)
	rdb, err := valkey.Client("default")
	if err != nil {
		return fmt.Errorf("valkey init: %w", err)
	}
	defer func() { _ = valkey.Close() }()

	r, err := buildRouter(cfg, logger, pool, rdb)
	if err != nil {
		return err
	}

	return r.Run(":" + cfg.Port)
}

// buildRouter builds the Gin engine: it installs the middleware stack in the
// documented order (see design.md §Middleware Stack), builds the per-IP throttle
// group, then registers every route. No business logic lives here.
func buildRouter(cfg *config.Config, logger *slog.Logger, pool *pgxpool.Pool, rdb *redis.Client) (*gin.Engine, error) {
	r := gin.New()

	// Middleware stack, outermost first
	middleware.Setup(r, cfg, logger)

	limit, window, err := throttle.ParseRate(defaultRate)
	if err != nil {
		return nil, fmt.Errorf("throttle rate %q: %w", defaultRate, err)
	}
	limited := r.Group("", middleware.Throttle(throttle.New(rdb), limit, window))

	registerRoutes(r, limited, pool)
	return r, nil
}

// registerRoutes is the single home for every HTTP route: infra routes on the
// root engine, domain routes on the throttled group.
func registerRoutes(r *gin.Engine, limited gin.IRouter, pool *pgxpool.Pool) {
	r.GET("/ping", func(c *gin.Context) {
		response.Success(c, 200, "pong", nil)
	})

	// Temporary route proving the error envelope; drops once real routes land.
	r.GET("/error", func(c *gin.Context) {
		response.Error(c, errs.NewNotFound(msgResourceNotFound))
	})

	items.NewHandler(items.NewService(pool)).RegisterRoutes(limited)
}
