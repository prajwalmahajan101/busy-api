package middleware

import "github.com/gin-gonic/gin"

// SecurityHeaders sets baseline security response headers on every request.
// HSTS is added only when env == "prod" — it is meaningless (and harmful to
// cache) over the plain HTTP used in local/dev.
func SecurityHeaders(env string) gin.HandlerFunc {
	prod := env == "prod"
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		if prod {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		}
		c.Next()
	}
}
