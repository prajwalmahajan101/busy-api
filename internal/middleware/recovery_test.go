package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryReturns500Envelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Recovery())
	r.GET("/boom", func(c *gin.Context) { panic("kaboom") })

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	body, err := io.ReadAll(w.Body)
	require.NoError(t, err)
	var env map[string]any
	require.NoError(t, json.Unmarshal(body, &env))
	assert.Equal(t, false, env["success"])

	list := env["errors"].([]any)
	require.Len(t, list, 1)
	assert.Equal(t, "infrastructure_error", list[0].(map[string]any)["code"])
}
