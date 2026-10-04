package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prajwalmahajan101/busyapi/internal/reqcontext"
)

// RequestLogging seeds a per-layer timing accumulator into the request context,
// then writes one structured access-log line after the handler returns, carrying
// handler/service/repo timings and the request id from reqcontext.
func RequestLogging(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := reqcontext.WithTiming(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)

		start := time.Now()
		c.Next()
		handlerMS := time.Since(start).Milliseconds()

		serviceMS, repoMS := reqcontext.TimingFromContext(ctx)
		logger.Info("request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"handler_ms", handlerMS,
			"service_ms", serviceMS,
			"repo_ms", repoMS,
			"request_id", reqcontext.RequestIDFromContext(ctx),
			"client_ip", c.ClientIP(),
		)
	}
}
