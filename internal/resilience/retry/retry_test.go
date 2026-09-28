package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prajwalmahajan101/busyapi/internal/errs"
)

var fastOpts = Options{MaxAttempts: 4, Base: time.Millisecond, Max: 5 * time.Millisecond}

func TestDo_SucceedsFirstTry(t *testing.T) {
	calls := 0
	err := Do(context.Background(), func() error {
		calls++
		return nil
	}, fastOpts)
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v, want calls=1 err=nil", calls, err)
	}
}

func TestDo_PermanentErrorStopsImmediately(t *testing.T) {
	calls := 0
	permanent := errs.NewValidation("bad", nil) // does not trip breaker
	err := Do(context.Background(), func() error {
		calls++
		return permanent
	}, fastOpts)
	if calls != 1 {
		t.Fatalf("calls=%d, want 1 (no retry on permanent error)", calls)
	}
	if !errors.Is(err, permanent) {
		t.Fatalf("err=%v, want permanent error returned", err)
	}
}

func TestDo_RetriesTransientThenExhausts(t *testing.T) {
	calls := 0
	transient := errs.NewTransient("boom") // retryable
	err := Do(context.Background(), func() error {
		calls++
		return transient
	}, fastOpts)
	if calls != fastOpts.MaxAttempts {
		t.Fatalf("calls=%d, want %d", calls, fastOpts.MaxAttempts)
	}
	if !errors.Is(err, transient) {
		t.Fatalf("err=%v, want last transient error", err)
	}
}

func TestDo_RecoversAfterTransient(t *testing.T) {
	calls := 0
	err := Do(context.Background(), func() error {
		calls++
		if calls < 3 {
			return errs.NewTransient("boom")
		}
		return nil
	}, fastOpts)
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v, want calls=3 err=nil", calls, err)
	}
}

func TestDo_ContextCancelledStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	calls := 0
	err := Do(ctx, func() error {
		calls++
		return errs.NewTransient("boom")
	}, fastOpts)
	if calls != 0 {
		t.Fatalf("calls=%d, want 0 (ctx cancelled before first try)", calls)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
}
