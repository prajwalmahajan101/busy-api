package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prajwalmahajan101/busyapi/internal/reqcontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestLoggingEmitsFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.Use(RequestLogging(logger))
	r.GET("/", func(c *gin.Context) {
		// RequestLogging has seeded the reqcontext timing accumulator onto the
		// request context; record into it the way the service/repo layers do.
		reqcontext.AddServiceTime(c.Request.Context(), 5*time.Millisecond)
		reqcontext.AddRepoTime(c.Request.Context(), 2*time.Millisecond)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(requestIDHeader, testInboundID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	assert.Equal(t, "request", line["msg"])
	assert.Equal(t, testInboundID, line["request_id"])
	assert.EqualValues(t, 200, line["status"])
	assert.Contains(t, line, "handler_ms")
	assert.EqualValues(t, 5, line["service_ms"])
	assert.EqualValues(t, 2, line["repo_ms"])
}
