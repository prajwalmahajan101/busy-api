package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prajwalmahajan101/busyapi/internal/reqcontext"
)

// timingWriter wraps gin's ResponseWriter to stamp the Server-Timing header the
// moment the response commits (first WriteHeader/Write). gin flushes headers as
// soon as the handler writes the body, so a c.Header() call *after* c.Next()
// would be a no-op — the header must be set before the first byte goes out. By
// commit time the service/repo accumulator is already populated (the handler
// finishes its DB work before writing the response).
type timingWriter struct {
	gin.ResponseWriter
	ctx     context.Context
	start   time.Time
	stamped bool
}

func (w *timingWriter) stamp() {
	if w.stamped {
		return
	}
	w.stamped = true
	serviceMS, repoMS := reqcontext.TimingFromContext(w.ctx)
	handlerMS := time.Since(w.start).Milliseconds()
	w.Header().Set("Server-Timing", fmt.Sprintf(
		"handler;dur=%d, service;dur=%d, repo;dur=%d",
		handlerMS, serviceMS, repoMS,
	))
}

func (w *timingWriter) WriteHeader(code int) {
	w.stamp()
	w.ResponseWriter.WriteHeader(code)
}

func (w *timingWriter) Write(b []byte) (int, error) {
	w.stamp()
	return w.ResponseWriter.Write(b)
}

func (w *timingWriter) WriteString(s string) (int, error) {
	w.stamp()
	return w.ResponseWriter.WriteString(s)
}

// RequestLogging seeds a per-layer timing accumulator into the request context,
// then writes one structured access-log line after the handler returns, carrying
// handler/service/repo timings and the request id from reqcontext. It also emits
// those timings as a standard Server-Timing response header so an HTTP client
// (e.g. k6) can attribute latency per layer.
func RequestLogging(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := reqcontext.WithTiming(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)

		start := time.Now()
		c.Writer = &timingWriter{ResponseWriter: c.Writer, ctx: ctx, start: start}
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
