package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/prajwalmahajan101/busyapi/internal/logging"
)

func main() {
	logger := logging.Setup()

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestLogger(logger))

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "pong"})
	})

	if err := r.Run(":8000"); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		id := uuid.NewString()
		ctx := logging.WithRequestID(c.Request.Context(), id)
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
