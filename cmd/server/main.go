package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/prajwalmahajan101/busyapi/internal/errs"
	"github.com/prajwalmahajan101/busyapi/internal/logging"
	"github.com/prajwalmahajan101/busyapi/internal/reqcontext"
	"github.com/prajwalmahajan101/busyapi/internal/response"
)

func main() {
	logger := logging.Setup()

	r := newRouter(logger)

	if err := r.Run(":8000"); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
}

// newRouter builds the Gin engine with the middleware stack and routes.
// Extracted so tests can drive routes via httptest without binding a port.
func newRouter(logger *slog.Logger) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestLogger(logger))

	r.GET("/ping", func(c *gin.Context) {
		response.Success(c, 200, "pong", nil)
	})

	// Temporary route proving the error envelope; drops once real routes land.
	r.GET("/error", func(c *gin.Context) {
		response.Error(c, errs.NewNotFound("resource not found"))
	})

	return r
}

func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		id := uuid.NewString()
		ctx := reqcontext.WithRequestID(c.Request.Context(), id)
		c.Request = c.Request.WithContext(ctx)
		c.Header("X-Request-ID", id)

		c.Next()

		logger.InfoContext(ctx, "request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}
