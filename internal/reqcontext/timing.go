package reqcontext

import (
	"context"
	"sync/atomic"
	"time"
)

// timing accumulates per-layer durations (nanoseconds) for one request. Stored
// as a *pointer* in context so AddServiceTime/AddRepoTime calls from the service
// and repository layers are visible to the middleware that reads the totals
// after the handler returns.
type timing struct {
	serviceNS atomic.Int64
	repoNS    atomic.Int64
}

type timingCtxKey struct{}

var timingKey = timingCtxKey{}

// WithTiming returns a copy of ctx carrying a fresh per-layer timing
// accumulator. The middleware seeds this once per request before the handler
// runs; layers below add to it through the same ctx.
func WithTiming(ctx context.Context) context.Context {
	return context.WithValue(ctx, timingKey, &timing{})
}

func timingFrom(ctx context.Context) *timing {
	t, _ := ctx.Value(timingKey).(*timing)
	return t
}

// AddServiceTime adds d to the service-layer total for this request. No-op when
// ctx carries no accumulator (e.g. a call made outside the HTTP path).
func AddServiceTime(ctx context.Context, d time.Duration) {
	if t := timingFrom(ctx); t != nil {
		t.serviceNS.Add(int64(d))
	}
}

// AddRepoTime adds d to the repository-layer total for this request.
func AddRepoTime(ctx context.Context, d time.Duration) {
	if t := timingFrom(ctx); t != nil {
		t.repoNS.Add(int64(d))
	}
}

// TrackService returns a stop func; defer it to add elapsed service time.
// Usage: defer reqcontext.TrackService(ctx)()
func TrackService(ctx context.Context) func() {
	start := time.Now()
	return func() { AddServiceTime(ctx, time.Since(start)) }
}

// TrackRepo returns a stop func for repository-layer timing. Call it right after
// the DB call (not deferred) so it measures the query, not the whole method.
func TrackRepo(ctx context.Context) func() {
	start := time.Now()
	return func() { AddRepoTime(ctx, time.Since(start)) }
}

// TimingFromContext returns the accumulated service and repo durations in
// milliseconds. Zero when ctx carries no accumulator.
func TimingFromContext(ctx context.Context) (serviceMS, repoMS int64) {
	t := timingFrom(ctx)
	if t == nil {
		return 0, 0
	}
	return t.serviceNS.Load() / int64(time.Millisecond), t.repoNS.Load() / int64(time.Millisecond)
}
