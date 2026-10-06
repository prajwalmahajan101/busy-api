package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prajwalmahajan101/busyapi/internal/config"
	"github.com/prajwalmahajan101/busyapi/internal/logging"
	"github.com/prajwalmahajan101/busyapi/internal/resilience/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func doGet(t *testing.T, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := testRouter()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body, err := io.ReadAll(w.Body)
	require.NoError(t, err)
	var env map[string]any
	require.NoError(t, json.Unmarshal(body, &env))
	return w, env
}

func TestPingSuccessEnvelope(t *testing.T) {
	w, env := doGet(t, "/ping")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, true, env["success"])
	assert.Equal(t, "pong", env["message"])
	assert.NotEmpty(t, env["request_id"])
	assert.NotEmpty(t, w.Header().Get("X-Request-ID"))
}

func TestErrorTypedErrorEnvelope(t *testing.T) {
	w, env := doGet(t, "/error")

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, false, env["success"])
	assert.NotEmpty(t, env["request_id"])

	errsList, ok := env["errors"].([]any)
	require.True(t, ok, "errors array present")
	require.Len(t, errsList, 1)
	first := errsList[0].(map[string]any)
	assert.Equal(t, "not_found", first["code"])
}

func testRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{MaxBodyBytes: 1 << 20, Env: "local"}
	return buildRouter(cfg, logging.Setup(), nil, cache.NewProvider(nil).Get("test"), nil)
}
