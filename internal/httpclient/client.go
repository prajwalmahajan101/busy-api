package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prajwalmahajan101/busyapi/internal/config"
	"github.com/prajwalmahajan101/busyapi/internal/errs"
	"github.com/prajwalmahajan101/busyapi/internal/resilience/registry"
	"github.com/prajwalmahajan101/busyapi/internal/sanitize"
)

// Auth kinds for outbound requests.
const (
	AuthNone   = "none"
	AuthBearer = "bearer"
	AuthBasic  = "basic"
	AuthAPIKey = "apikey"
)

// defaultAPIKeyHeader is used when an apikey Auth leaves HeaderName empty.
const defaultAPIKeyHeader = "X-API-Key"

// Auth describes per-call authentication applied to outbound requests.
type Auth struct {
	Kind       string // AuthNone | AuthBearer | AuthBasic | AuthAPIKey (empty == none)
	Token      string // bearer token or apikey value
	User, Pass string // basic auth
	HeaderName string // apikey header name (default defaultAPIKeyHeader)
}

func (a Auth) apply(r *http.Request) {
	switch a.Kind {
	case AuthBearer:
		r.Header.Set("Authorization", "Bearer "+a.Token)
	case AuthBasic:
		r.SetBasicAuth(a.User, a.Pass)
	case AuthAPIKey:
		name := a.HeaderName
		if name == "" {
			name = defaultAPIKeyHeader
		}
		r.Header.Set(name, a.Token)
	}
}

// Client is a hardened outbound HTTP client scoped to one upstream service:
// pooled transport, SSRF guard at dial time, per-call auth, error mapping onto
// the errs hierarchy, and every call wrapped in the resilience registry.
type Client struct {
	hc           *http.Client
	reg          *registry.Registry
	name         string
	auth         Auth
	allowPrivate bool
	maxValueLen  int
	maxBodyBytes int64
}

// Response is the mapped result of a successful (2xx/3xx) call.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Option customizes a single call.
type Option func(*http.Request)

// WithHeader sets a request header for this call.
func WithHeader(key, val string) Option {
	return func(r *http.Request) { r.Header.Set(key, val) }
}

// WithAuth overrides the client's default auth for this call.
func WithAuth(a Auth) Option {
	return func(r *http.Request) { a.apply(r) }
}

// New builds a Client for the named service. reg may be nil (no breaker/retry).
// auth is the default applied to every request.
func New(cfg *config.Config, reg *registry.Registry, name string, auth Auth) *Client {
	dialer := &net.Dialer{
		Timeout: time.Duration(cfg.HTTPDialTimeoutMS) * time.Millisecond,
		Control: dialControl(cfg.HTTPAllowPrivateIPs),
	}
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         dialer.DialContext,
		MaxIdleConns:        cfg.HTTPMaxIdleConns,
		MaxIdleConnsPerHost: cfg.HTTPMaxIdleConnsPerHost,
		IdleConnTimeout:     time.Duration(cfg.HTTPIdleConnTimeoutS) * time.Second,
		ForceAttemptHTTP2:   true,
	}
	return &Client{
		hc: &http.Client{
			Transport: transport,
			Timeout:   time.Duration(cfg.HTTPTimeoutMS) * time.Millisecond,
		},
		reg:          reg,
		name:         name,
		auth:         auth,
		allowPrivate: cfg.HTTPAllowPrivateIPs,
		maxValueLen:  cfg.LogMaxValueLen,
		maxBodyBytes: cfg.MaxBodyBytes,
	}
}

// Get is a convenience wrapper for a GET call.
func (c *Client) Get(ctx context.Context, rawURL string, opts ...Option) (*Response, error) {
	return c.Do(ctx, http.MethodGet, rawURL, nil, opts...)
}

// PostJSON marshals v to JSON and POSTs it with the correct content type.
func (c *Client) PostJSON(ctx context.Context, rawURL string, v any, opts ...Option) (*Response, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, errs.NewValidation("marshal request body", map[string]any{"error": err.Error()})
	}
	opts = append(opts, WithHeader("Content-Type", "application/json"))
	return c.Do(ctx, http.MethodPost, rawURL, payload, opts...)
}

// Do performs method+url with an optional body, under the service's breaker+retry.
// It applies default auth (then any WithAuth override), enforces the SSRF guard,
// and maps the outcome onto the errs hierarchy:
//
//	timeout               -> ExternalTimeout (retryable, trips breaker)
//	5xx                   -> Transient       (retryable, trips breaker)
//	4xx                   -> External        (not retryable)
//	transport/dial error  -> Transient       (retryable)
//
// body is passed as bytes so each retry attempt rebuilds a fresh reader.
// A returned *Response is non-nil only on a 2xx/3xx status.
func (c *Client) Do(ctx context.Context, method, rawURL string, body []byte, opts ...Option) (*Response, error) {
	if err := AssertPublicURL(rawURL, c.allowPrivate); err != nil {
		return nil, err
	}
	var resp *Response
	err := c.runResilient(ctx, func() error {
		var e error
		resp, e = c.attempt(ctx, method, rawURL, body, opts)
		return e
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// runResilient runs fn under the service breaker+retry when a registry is set,
// otherwise directly.
func (c *Client) runResilient(ctx context.Context, fn func() error) error {
	if c.reg != nil {
		return c.reg.Resilient(ctx, c.name, fn)
	}
	return fn()
}

// attempt runs one HTTP round-trip: build, auth, send, read, map. Retry calls it
// afresh each time, so it must not depend on prior state.
func (c *Client) attempt(ctx context.Context, method, rawURL string, body []byte, opts []Option) (*Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, errs.NewValidation("build request", map[string]any{"error": err.Error()})
	}
	c.auth.apply(req)
	for _, opt := range opts {
		opt(req)
	}

	start := time.Now()
	httpResp, err := c.hc.Do(req)
	dur := time.Since(start)
	if err != nil {
		return nil, c.mapDoError(ctx, req, err, dur)
	}
	defer httpResp.Body.Close()

	respBody, rerr := io.ReadAll(io.LimitReader(httpResp.Body, c.maxBodyBytes))
	if rerr != nil {
		return nil, errs.NewTransient("read response body: " + rerr.Error())
	}
	c.log(ctx, req, httpResp.StatusCode, dur, respBody)

	if serr := c.statusError(httpResp.StatusCode); serr != nil {
		return nil, serr
	}
	return &Response{Status: httpResp.StatusCode, Header: httpResp.Header, Body: respBody}, nil
}

// statusError maps a response status to the errs hierarchy: 5xx transient
// (retryable), 4xx external (permanent), otherwise nil (success).
func (c *Client) statusError(status int) error {
	switch {
	case status >= 500:
		return errs.NewTransient(fmt.Sprintf("%s returned %d", c.name, status))
	case status >= 400:
		return errs.NewExternal(fmt.Sprintf("%s returned %d", c.name, status))
	}
	return nil
}

// mapDoError classifies a transport-level failure from http.Client.Do.
func (c *Client) mapDoError(ctx context.Context, req *http.Request, err error, dur time.Duration) error {
	host := SafeHost(req.URL.String())
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		slog.WarnContext(ctx, "outbound timeout",
			"service", c.name, "host", host, "duration_ms", dur.Milliseconds(), "error", err.Error())
		return errs.NewExternalTimeout(c.name + " timeout")
	default:
		slog.WarnContext(ctx, "outbound transport error",
			"service", c.name, "host", host, "duration_ms", dur.Milliseconds(), "error", err.Error())
		return errs.NewTransient(c.name + " transport error: " + err.Error())
	}
}

func (c *Client) log(ctx context.Context, req *http.Request, status int, dur time.Duration, body []byte) {
	slog.InfoContext(ctx, "outbound request",
		"service", c.name,
		"method", req.Method,
		"host", SafeHost(req.URL.String()),
		"status", status,
		"duration_ms", dur.Milliseconds(),
		"response", c.sanitizeBody(body),
	)
}

// sanitizeBody parses a JSON body and masks sensitive fields; falls back to a
// truncated raw string for non-JSON.
func (c *Client) sanitizeBody(body []byte) any {
	if len(body) == 0 {
		return ""
	}
	var parsed any
	if json.Unmarshal(body, &parsed) == nil {
		return sanitize.Sanitize(parsed, c.maxValueLen)
	}
	return sanitize.Sanitize(body, c.maxValueLen)
}
