package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prajwalmahajan101/busyapi/internal/reqcontext"
)

// gin context keys for per-layer timing accumulation.
const (
	ctxServiceMS = "timing_service_ms"
	ctxRepoMS    = "timing_repo_ms"
)

// RequestLogging writes one structured access-log line after the handler
// returns, carrying per-layer timings and the request id from reqcontext.
func RequestLogging(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		handlerMS := time.Since(start).Milliseconds()

		ctx := c.Request.Context()
		logger.Info("request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"handler_ms", handlerMS,
			"service_ms", c.GetInt64(ctxServiceMS),
			"repo_ms", c.GetInt64(ctxRepoMS),
			"request_id", reqcontext.RequestIDFromContext(ctx),
			"client_ip", c.ClientIP(),
		)
	}
}

// AddServiceTime accumulates service-layer duration for the current request,
// surfaced as service_ms in the access log.
func AddServiceTime(c *gin.Context, d time.Duration) {
	c.Set(ctxServiceMS, c.GetInt64(ctxServiceMS)+d.Milliseconds())
}

// AddRepoTime accumulates repository-layer duration, surfaced as repo_ms.
func AddRepoTime(c *gin.Context, d time.Duration) {
	c.Set(ctxRepoMS, c.GetInt64(ctxRepoMS)+d.Milliseconds())
}
