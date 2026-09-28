// Package retry re-runs a function with exponential backoff + jitter while the
// error is retryable (transient / breaker-tripping), stopping early on a
// permanent error or context cancellation (R12, NFR-R1).
package retry

import (
	"context"
	"math"
	"math/rand/v2"
	"time"

	"github.com/prajwalmahajan101/busyapi/internal/errs"
)

// Options tunes the backoff. Base doubles each attempt, capped at Max, then
// scaled by jitter in [0.5, 1.5). MaxAttempts counts the first try.
type Options struct {
	MaxAttempts int
	Base        time.Duration
	Max         time.Duration
}

// Do calls fn until it succeeds, the error is not retryable, attempts run out,
// or ctx is cancelled. Returns the last error.
func Do(ctx context.Context, fn func() error, opts Options) error {
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = 1
	}
	var err error
	for attempt := 0; attempt < opts.MaxAttempts; attempt++ {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		err = fn()
		if err == nil {
			return nil
		}
		if !retryable(err) {
			return err
		}
		if attempt == opts.MaxAttempts-1 {
			break // last attempt - no trailing sleep
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff(opts.Base, opts.Max, attempt)):
		}
	}
	return err
}

// retryable reports whether err is worth another attempt. Transient errors trip
// the breaker, so TripsBreaker is the single predicate (transient ⊆ tripping).
func retryable(err error) bool {
	return errs.TripsBreaker(err)
}

// backoff = min(base × 2^attempt, max) × jitter[0.5, 1.5).
func backoff(base, max time.Duration, attempt int) time.Duration {
	d := base
	if attempt >= 63 || base > max>>attempt {
		d = max
	} else {
		d = min(base<<attempt, max)
	}

	jitter := 0.5 + rand.Float64() //nolint:gosec // backoff jitter, not security-sensitive
	scaled := float64(d) * jitter
	if scaled > math.MaxInt64 {
		return max
	}
	return time.Duration(scaled)
}
