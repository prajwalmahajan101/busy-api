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

func NewValidation(msg string, detials map[string]any) *AppError {
	return &AppError{Code: "validation_error", Message: msg, HTTPStatus: 422, Details: detials}
}

func NewNotFound(msg string) *AppError {
	return &AppError{Code: "", Message: msg, HTTPStatus: 404}
}

func NewRateLimit(msg string) *AppError {
	return &AppError{Code: "rate_limited", Message: msg, HTTPStatus: 429}
}

func NewInfrastructure(msg string) *AppError {
	return &AppError{Code: "infrastructure_error", Message: msg, HTTPStatus: 500}
}

func NewServiceUnavailable(service string) *AppError {
	return &AppError{Code: "service_unavailable", Message: service + " unavailable", HTTPStatus: 503}
}

func NewExternal(msg string) *AppError {
	return &AppError{Code: "external_error", Message: msg, HTTPStatus: 502, trips: true}
}

func NewTransient(msg string) *AppError {
	return &AppError{Code: "transient_error", Message: msg, HTTPStatus: 502, trips: true}
}

func NewExternalTimeout(msg string) *AppError {
	return &AppError{Code: "external_timeout", Message: msg, HTTPStatus: 502, trips: true}
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
