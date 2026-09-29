package middleware

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// RateLimitResult is the minimal rate-limit data RateLimitHeaders emits. The
// throttle middleware populates it via SetRateLimitResult; this package owns
// the contract so throttle need not be imported here.
type RateLimitResult struct {
	Limit     int   // requests allowed in the window
	Remaining int   // requests left in the current window
	Reset     int64 // unix seconds when the window resets
}

// SetRateLimitResult stores r on the gin context for RateLimitHeaders to emit.
// Called by the throttle middleware upstream of RateLimitHeaders.
func SetRateLimitResult(c *gin.Context, r RateLimitResult) {
	c.Set(ctxRateLimit, r)
}

// WriteRateLimitHeaders emits the three X-RateLimit-* headers. It must be called
// before the response body is written; the throttle middleware calls it directly
// so the headers survive on the 429 path too.
func WriteRateLimitHeaders(c *gin.Context, r RateLimitResult) {
	h := c.Writer.Header()
	h.Set("X-RateLimit-Limit", strconv.Itoa(r.Limit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(r.Remaining))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(r.Reset, 10))
}

// RateLimitHeaders writes X-RateLimit-Limit/Remaining/Reset from a
// RateLimitResult previously stored on the context. No-op when absent, so it is
// safe on endpoints without rate limiting.
func RateLimitHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		if v, ok := c.Get(ctxRateLimit); ok {
			if r, ok := v.(RateLimitResult); ok {
				WriteRateLimitHeaders(c, r)
			}
		}
		c.Next()
	}
}
