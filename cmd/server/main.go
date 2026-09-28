package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/gin-gonic/gin"

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

func main() {
	logger := logging.Setup()

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}

	pool, err := db.NewPool(context.Background(), cfg)
	if err != nil {
		logger.Error("db pool init failed", "err", err)
		os.Exit(1)
	}
	defer db.Close(pool)

	valkey.Configure(cfg.ValkeyURL)
	rdb, err := valkey.Client("default")
	if err != nil {
		logger.Error("valkey init failed", "err", err)
		os.Exit(1)
	}
	defer func() { _ = valkey.Close() }()

	throttler := throttle.New(rdb)
	limit, window, err := throttle.ParseRate("100/min")
	if err != nil {
		logger.Error("bad rate", "err", err)
		os.Exit(1)
	}

	r := newRouter(cfg, logger)
	limited := r.Group("", middleware.Throttle(throttler, limit, window))
	items.NewHandler(items.NewService(pool)).RegisterRoutes(limited)

	if err := r.Run(":" + cfg.Port); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
}

// newRouter builds the Gin engine: it installs the middleware stack in the
// documented order (see design.md §Middleware Stack) then registers routes.
// No business logic lives here.
func newRouter(cfg *config.Config, logger *slog.Logger) *gin.Engine {
	r := gin.New()

	// Middleware stack, outermost first

	r.Use(middleware.Recovery())
	r.Use(middleware.BodyLimit(cfg.MaxBodyBytes))
	r.Use(middleware.CORS(cfg.CORSOrigins))
	r.Use(middleware.SecurityHeaders(cfg.Env))
	r.Use(middleware.RequestID())
	r.Use(middleware.RequestLogging(logger))
	r.Use(middleware.RateLimitHeaders())

	registerRoutes(r)
	return r
}

// registerRoutes wires the HTTP routes. Real domain routes land in Phase 3.
func registerRoutes(r *gin.Engine) {
	r.GET("/ping", func(c *gin.Context) {
		response.Success(c, 200, "pong", nil)
	})

	// Temporary route proving the error envelope; drops once real routes land.
	r.GET("/error", func(c *gin.Context) {
		response.Error(c, errs.NewNotFound("resource not found"))
	})
}
