// Package reqcontext carries the per-request id through context.Context.
// It sits at the base of the dependency graph — it imports only the stdlib
// (context), so logging, response, and errs can all read the request id
// without importing each other or creating an import cycle.
package reqcontext

import "context"

// ctxKey is an unexported type so no other package can collide with our
// context key. The zero value is the single key we use.
type ctxKey struct{}

var requestIDKey = ctxKey{}

// WithRequestID returns a copy of ctx carrying the request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext reads the request id from ctx, returning "" if absent.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}
