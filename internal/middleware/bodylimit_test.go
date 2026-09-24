package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drains the body so MaxBytesReader can trip, pushing any error to gin.
func drainHandler(c *gin.Context) {
	if _, err := io.ReadAll(c.Request.Body); err != nil {
		_ = c.Error(err)
		return
	}
	c.Status(http.StatusOK)
}

func newBodyLimitRouter(max int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit(max))
	r.POST("/", drainHandler)
	return r
}

func assert413(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	var env map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	assert.Equal(t, false, env["success"])
	list := env["errors"].([]any)
	require.Len(t, list, 1)
	assert.Equal(t, "payload_too_large", list[0].(map[string]any)["code"])
}

func TestBodyLimitRejectsByContentLength(t *testing.T) {
	r := newBodyLimitRouter(10)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(make([]byte, 100)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert413(t, w)
}

func TestBodyLimitCapsWhenContentLengthUnset(t *testing.T) {
	r := newBodyLimitRouter(10)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(make([]byte, 100)))
	req.ContentLength = 0 // force the MaxBytesReader path
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert413(t, w)
}

func TestBodyLimitAllowsSmallBody(t *testing.T) {
	r := newBodyLimitRouter(100)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(make([]byte, 10)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}
