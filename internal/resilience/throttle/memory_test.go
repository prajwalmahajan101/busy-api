package throttle

import (
	"context"
	"testing"
	"time"
)

func TestMemoryThrottle_LimitAndSlide(t *testing.T) {
	th := newMemoryThrottle()
	ctx := context.Background()
	const id, limit = "ip:1.2.3.4", 3
	window := 50 * time.Millisecond

	// First `limit` calls allowed; remaining counts down to 0.
	for i := 1; i <= limit; i++ {
		r, _ := th.Check(ctx, id, limit, window)
		if !r.Allowed {
			t.Fatalf("call %d: not allowed, want allowed", i)
		}
		if r.Remaining != limit-i {
			t.Fatalf("call %d: remaining = %d, want %d", i, r.Remaining, limit-i)
		}
	}

	// Next call over the limit → rejected.
	if r, _ := th.Check(ctx, id, limit, window); r.Allowed {
		t.Fatal("over-limit call allowed, want rejected")
	}

	// After the window slides, the log clears and calls are allowed again.
	time.Sleep(60 * time.Millisecond)
	if r, _ := th.Check(ctx, id, limit, window); !r.Allowed {
		t.Fatal("post-window call rejected, want allowed")
	}
}
