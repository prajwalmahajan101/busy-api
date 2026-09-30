// Package errs defines the typed application error hierarchy. Each error carries
// an HTTP status, a stable machine code, and a breaker predicate used by the
// resilience layer. Stdlib-only by design — the gin error-handler middleware
// lives in internal/response to avoid an import cycle (response imports errs).
package errs

import "errors"

// AppError is the single concrete error type returned across the app.

type AppError struct {
	Code       string
	Message    string
	HTTPStatus int
	Details    map[string]any
	RequestID  string

	trips bool // whether this error counts toward opening the circuit breaker
}

func (e *AppError) Error() string {
	return e.Code + ": " + e.Message
}

// Code values are stable machine-readable error identifiers, shared across the
// app and the API error envelope. The single source of truth for error codes.
const (
	CodeValidation      = "validation_error"
	CodeNotFound        = "not_found"
	CodeRateLimited     = "rate_limited"
	CodeInfrastructure  = "infrastructure_error"
	CodeUnavailable     = "service_unavailable"
	CodeExternal        = "external_error"
	CodeTransient       = "transient_error"
	CodeExternalTimeout = "external_timeout"
)

func NewValidation(msg string, details map[string]any) *AppError {
	return &AppError{Code: CodeValidation, Message: msg, HTTPStatus: 422, Details: details}
}

func NewNotFound(msg string) *AppError {
	return &AppError{Code: CodeNotFound, Message: msg, HTTPStatus: 404}
}

func NewRateLimit(msg string) *AppError {
	return &AppError{Code: CodeRateLimited, Message: msg, HTTPStatus: 429}
}

func NewInfrastructure(msg string) *AppError {
	return &AppError{Code: CodeInfrastructure, Message: msg, HTTPStatus: 500}
}

func NewServiceUnavailable(service string) *AppError {
	return &AppError{Code: CodeUnavailable, Message: service + " unavailable", HTTPStatus: 503}
}

// NewExternal marks an upstream that rejected our request (e.g. a 4xx). The
// upstream is healthy, so this neither retries nor trips the breaker.
func NewExternal(msg string) *AppError {
	return &AppError{Code: CodeExternal, Message: msg, HTTPStatus: 502}
}

func NewTransient(msg string) *AppError {
	return &AppError{Code: CodeTransient, Message: msg, HTTPStatus: 502, trips: true}
}

func NewExternalTimeout(msg string) *AppError {
	return &AppError{Code: CodeExternalTimeout, Message: msg, HTTPStatus: 502, trips: true}
}

// TripsBreaker reports whether err (or any AppError it wraps) should count as a
// failure toward opening the circuit breaker.
func TripsBreaker(err error) bool {
	var ae *AppError
	if errors.As(err, &ae) {
		return ae.trips
	}
	return false
}
