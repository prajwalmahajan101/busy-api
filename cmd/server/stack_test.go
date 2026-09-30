package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prajwalmahajan101/busyapi/internal/config"
	"github.com/prajwalmahajan101/busyapi/internal/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const reqIDHeader = "X-Request-ID"

func routerWithMaxBody(maxBytes int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{MaxBodyBytes: maxBytes, Env: "local"}
	return buildRouter(cfg, logging.Setup(), nil)
}

func TestStackOversizedBodyReturns413(t *testing.T) {
	r := routerWithMaxBody(10)
	req := httptest.NewRequest(http.MethodPost, "/ping", bytes.NewReader(make([]byte, 100)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	var env map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	assert.Equal(t, false, env["success"])
	list := env["errors"].([]any)
	require.Len(t, list, 1)
	assert.Equal(t, "payload_too_large", list[0].(map[string]any)["code"])
}

func TestStackSetsSecurityHeadersAndRequestID(t *testing.T) {
	r := routerWithMaxBody(1 << 20)
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	h := w.Header()
	assert.NotEmpty(t, h.Get(reqIDHeader))
	assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", h.Get("X-Frame-Options"))
	assert.NotEmpty(t, h.Get("Referrer-Policy"))
	assert.NotEmpty(t, h.Get("Content-Security-Policy"))
}

func TestStackEchoesInboundRequestID(t *testing.T) {
	r := routerWithMaxBody(1 << 20)
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set(reqIDHeader, "inbound-123")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, "inbound-123", w.Header().Get(reqIDHeader))
}
