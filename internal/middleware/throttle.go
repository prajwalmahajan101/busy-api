package middleware

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/prajwalmahajan101/busyapi/internal/errs"
	"github.com/prajwalmahajan101/busyapi/internal/resilience/throttle"
	"github.com/prajwalmahajan101/busyapi/internal/response"
)

// Throttle rate-limits per client IP against limit/window. It emits the
// X-RateLimit-* headers and aborts with 429 when the limit is exceeded. A
// throttle backend error fails open.
func Throttle(t throttle.Throttler, limit int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := throttle.Key(throttle.ScopeIP, c.ClientIP())
		res, err := t.Check(c.Request.Context(), id, limit, window)
		if err != nil {
			c.Next() // fail-open: a throttle backend error never blocks traffic
			return
		}
		// Write headers directly (not deferred) so they survive on the 429 path,
		// where response.Error writes the body immediately.
		WriteRateLimitHeaders(c, RateLimitResult{Limit: res.Limit, Remaining: res.Remaining, Reset: res.Reset})
		if !res.Allowed {
			response.Error(c, errs.NewRateLimit("rate limit exceeded"))
			c.Abort()
			return
		}
		c.Next()
	}
}
