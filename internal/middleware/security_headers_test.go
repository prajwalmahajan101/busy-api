package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func newSecHeadersRouter(env string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SecurityHeaders(env))
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestSecurityHeadersAlwaysSet(t *testing.T) {
	r := newSecHeadersRouter("dev")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	h := w.Header()
	assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", h.Get("X-Frame-Options"))
	assert.NotEmpty(t, h.Get("Referrer-Policy"))
	assert.NotEmpty(t, h.Get("Content-Security-Policy"))
	assert.NotEmpty(t, h.Get("Permissions-Policy"))
	assert.Empty(t, h.Get("Strict-Transport-Security"))
}

func TestSecurityHeadersHSTSInProd(t *testing.T) {
	r := newSecHeadersRouter("prod")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.NotEmpty(t, w.Header().Get("Strict-Transport-Security"))
}
