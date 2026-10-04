// Package middleware holds the ordered HTTP middleware chain and the Setup
// builder that installs it. Each middleware is a gin.HandlerFunc factory taking
// only what it needs; Setup is the composition layer that fixes the order.
package middleware

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/prajwalmahajan101/busyapi/internal/config"
)

// Error messages emitted by middleware, named so tests assert on the same string.
const (
	msgRateLimitExceeded = "rate limit exceeded"
	msgInternalServer    = "internal server error"
	msgBodyTooLarge      = "request body too large"
)

// String keys owned by this package: the request-id header and the gin context
// keys the middleware chain reads and writes.
const (
	// requestIDHeader carries the request id inbound and outbound.
	requestIDHeader = "X-Request-ID"
	// ctxRateLimit is where the throttle layer stores the per-request rate-limit
	// result; RateLimitHeaders reads it. Keeping the key here decouples
	// RateLimitHeaders from the throttle package.
	ctxRateLimit = "rate_limit_result"
)

// Setup installs the global middleware chain onto r in required order: Recovery
// outermost (catches panics from everything inside), then BodyLimit, CORS,
// SecurityHeaders, RequestID (before logging so access logs carry the id),
// RequestLogging, and RateLimitHeaders innermost. Route-scoped middleware
// (e.g. Throttle on a group) stays at the call site.
func Setup(r *gin.Engine, cfg *config.Config, logger *slog.Logger) {
	r.Use(Recovery())
	r.Use(BodyLimit(cfg.MaxBodyBytes))
	r.Use(CORS(cfg.CORSOrigins))
	r.Use(SecurityHeaders(cfg.Env))
	r.Use(RequestID())
	r.Use(RequestLogging(logger))
	r.Use(RateLimitHeaders())
}
