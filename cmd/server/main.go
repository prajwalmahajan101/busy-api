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
	"github.com/prajwalmahajan101/busyapi/internal/response"
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

	r := newRouter(cfg, logger)
	items.NewHandler(items.NewService(pool)).RegisterRoutes(r)

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
