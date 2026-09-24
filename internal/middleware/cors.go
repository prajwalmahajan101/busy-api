package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// CORS sets Access-Control-* headers for requests whose Origin is in the
// allowlist. It is a no-op when origins is empty. A preflight OPTIONS from an
// allowed origin short-circuits with 204; from a disallowed origin, 403.
func CORS(origins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}
	return func(c *gin.Context) {
		if len(allowed) == 0 {
			c.Next()
			return
		}
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}

		if _, ok := allowed[origin]; !ok {
			if c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.Next()
			return
		}

		h := c.Writer.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Vary", "Origin")
		h.Set("Access-Control-Allow-Credentials", "true")

		if c.Request.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			reqHeaders := c.GetHeader("Access-Control-Request-Headers")
			if reqHeaders == "" {
				reqHeaders = "Content-Type, Authorization, X-Request-ID"
			}
			h.Set("Access-Control-Allow-Headers", reqHeaders)
			h.Set("Access-Control-Max-Age", "600")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
