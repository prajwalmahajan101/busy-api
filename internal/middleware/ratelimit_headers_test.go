package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRateLimitHeadersEmitsFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Simulate the throttle middleware populating the result upstream.
	r.Use(func(c *gin.Context) {
		SetRateLimitResult(c, RateLimitResult{Limit: 100, Remaining: 42, Reset: 1700000000})
		c.Next()
	})
	r.Use(RateLimitHeaders())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	h := w.Header()
	assert.Equal(t, "100", h.Get("X-RateLimit-Limit"))
	assert.Equal(t, "42", h.Get("X-RateLimit-Remaining"))
	assert.Equal(t, "1700000000", h.Get("X-RateLimit-Reset"))
}

func TestRateLimitHeadersNoOpWhenAbsent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitHeaders())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Empty(t, w.Header().Get("X-RateLimit-Limit"))
}
