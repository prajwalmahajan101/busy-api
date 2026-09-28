package throttle

import (
	"context"
	"testing"
	"time"
)

func TestParseRate(t *testing.T) {
	ok := []struct {
		in     string
		limit  int
		window time.Duration
	}{
		{"100/min", 100, time.Minute},
		{"5/sec", 5, time.Second},
		{"10 / seconds", 10, time.Second},
		{"1000/hour", 1000, time.Hour},
		{"20/m", 20, time.Minute},
	}
	for _, tc := range ok {
		n, w, err := ParseRate(tc.in)
		if err != nil || n != tc.limit || w != tc.window {
			t.Fatalf("ParseRate(%q) = (%d, %v, %v), want (%d, %v, nil)", tc.in, n, w, err, tc.limit, tc.window)
		}
	}

	bad := []string{"", "100", "abc/min", "0/min", "-1/sec", "100/day", "100/"}
	for _, in := range bad {
		if _, _, err := ParseRate(in); err == nil {
			t.Fatalf("ParseRate(%q) = nil err, want error", in)
		}
	}
}

func TestKey(t *testing.T) {
	if got := Key(ScopeEndpoint, "POST", "/items"); got != "ep:POST:/items" {
		t.Fatalf("Key = %q", got)
	}
	if got := Key(ScopeGlobal); got != "global" {
		t.Fatalf("Key no-parts = %q", got)
	}
}

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
