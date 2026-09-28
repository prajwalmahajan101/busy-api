package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/prajwalmahajan101/busyapi/internal/resilience/throttle"
	"github.com/prajwalmahajan101/busyapi/internal/response"
)

func TestThrottle_429AfterLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitHeaders())
	limited := r.Group("", Throttle(throttle.New(nil), 2, time.Minute))
	limited.GET("/x", func(c *gin.Context) { response.Success(c, http.StatusOK, "ok", nil) })

	do := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "1.2.3.4:5555"
		r.ServeHTTP(w, req)
		return w
	}

	if w := do(); w.Code != http.StatusOK {
		t.Fatalf("call 1 = %d, want 200", w.Code)
	}
	if w := do(); w.Code != http.StatusOK {
		t.Fatalf("call 2 = %d, want 200", w.Code)
	}

	w := do() // 3rd exceeds limit
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("call 3 = %d, want 429", w.Code)
	}
	if got := w.Header().Get("X-RateLimit-Limit"); got != "2" {
		t.Fatalf("X-RateLimit-Limit = %q, want 2", got)
	}
	if got := w.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Fatalf("X-RateLimit-Remaining = %q, want 0", got)
	}
	if w.Header().Get("X-RateLimit-Reset") == "" {
		t.Fatal("X-RateLimit-Reset missing")
	}
}
