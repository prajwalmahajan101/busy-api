package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/prajwalmahajan101/busyapi/internal/reqcontext"
	"github.com/stretchr/testify/assert"
)

const testInboundID = "abc-123"

func newRequestIDRouter() (*gin.Engine, *string) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	var seen string
	r.GET("/", func(c *gin.Context) {
		seen = reqcontext.RequestIDFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})
	return r, &seen
}

func TestRequestIDAdoptsValidInbound(t *testing.T) {
	r, seen := newRequestIDRouter()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(requestIDHeader, testInboundID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, testInboundID, w.Header().Get(requestIDHeader))
	assert.Equal(t, testInboundID, *seen)
}

func TestRequestIDMintsWhenInvalid(t *testing.T) {
	r, seen := newRequestIDRouter()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(requestIDHeader, "bad id with spaces")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	got := w.Header().Get(requestIDHeader)
	assert.NotEqual(t, "bad id with spaces", got)
	_, err := uuid.Parse(got)
	assert.NoError(t, err)
	assert.Equal(t, got, *seen)
}

func TestRequestIDMintsWhenAbsent(t *testing.T) {
	r, seen := newRequestIDRouter()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	got := w.Header().Get(requestIDHeader)
	_, err := uuid.Parse(got)
	assert.NoError(t, err)
	assert.NotEmpty(t, *seen)
}

func TestRequestIDMintsWhenTooLong(t *testing.T) {
	r, _ := newRequestIDRouter()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(requestIDHeader, strings.Repeat("a", 129))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	_, err := uuid.Parse(w.Header().Get(requestIDHeader))
	assert.NoError(t, err)
}
