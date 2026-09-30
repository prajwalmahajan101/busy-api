package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prajwalmahajan101/busyapi/internal/config"
	"github.com/prajwalmahajan101/busyapi/internal/errs"
	"github.com/prajwalmahajan101/busyapi/internal/resilience/registry"
)

func testConfig() *config.Config {
	return &config.Config{
		HTTPTimeoutMS:           2000,
		HTTPDialTimeoutMS:       1000,
		HTTPMaxIdleConns:        10,
		HTTPMaxIdleConnsPerHost: 2,
		HTTPIdleConnTimeoutS:    30,
		HTTPAllowPrivateIPs:     true, // httptest binds 127.0.0.1
		LogMaxValueLen:          256,
		MaxBodyBytes:            1 << 20,
		RetryMax:                3,
		RetryBaseMS:             1,
		BreakerFailThreshold:    100, // don't open during these tests
		BreakerRecoveryS:        60,
	}
}

func newClient(cfg *config.Config, name string) *Client {
	return New(cfg, registry.New(cfg, nil), name, Auth{Kind: AuthNone})
}

func TestDo_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	resp, err := newClient(testConfig(), "ok-svc").Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	if string(resp.Body) != `{"ok":true}` {
		t.Fatalf("body = %q", resp.Body)
	}
}

func TestDo_5xxIsTransientAndRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()

	cfg := testConfig() // RetryMax=3
	_, err := newClient(cfg, "flaky-svc").Get(context.Background(), srv.URL)
	if !errs.TripsBreaker(err) {
		t.Fatalf("want retryable transient error, got %v", err)
	}
	if got := calls.Load(); got != int32(cfg.RetryMax) {
		t.Fatalf("server calls = %d, want %d (retried)", got, cfg.RetryMax)
	}
}

func TestDo_4xxIsExternalNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(401)
	}))
	defer srv.Close()

	_, err := newClient(testConfig(), "auth-svc").Get(context.Background(), srv.URL)
	var ae *errs.AppError
	if !errors.As(err, &ae) || ae.Code != errs.CodeExternal {
		t.Fatalf("want %s, got %v", errs.CodeExternal, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("server calls = %d, want 1 (no retry)", got)
	}
}

func TestDo_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	cfg := testConfig()
	cfg.HTTPTimeoutMS = 50
	cfg.RetryMax = 1
	_, err := newClient(cfg, "slow-svc").Get(context.Background(), srv.URL)
	var ae *errs.AppError
	if !errors.As(err, &ae) || ae.Code != errs.CodeExternalTimeout {
		t.Fatalf("want %s, got %v", errs.CodeExternalTimeout, err)
	}
}

func TestDo_RejectsPrivateURL(t *testing.T) {
	cfg := testConfig()
	cfg.HTTPAllowPrivateIPs = false
	_, err := newClient(cfg, "ssrf-svc").Get(context.Background(), "http://127.0.0.1:9/x")
	var ae *errs.AppError
	if !errors.As(err, &ae) || ae.Code != errs.CodeValidation {
		t.Fatalf("want %s, got %v", errs.CodeValidation, err)
	}
}

func TestPostJSON_SendsBody(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	_, err := newClient(testConfig(), "post-svc").PostJSON(context.Background(), srv.URL, map[string]any{"a": 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("body = %q, want {\"a\":1}", got)
	}
}
