package throttle

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type valkeyThrottle struct {
	rdb *redis.Client
	mem *memoryThrottle // fallback when Valkey is unreachable
}

func newValkeyThrottle(rdb *redis.Client) *valkeyThrottle {
	return &valkeyThrottle{rdb: rdb, mem: newMemoryThrottle()}
}

func (t *valkeyThrottle) Check(ctx context.Context, id string, limit int, window time.Duration) (Result, error) {
	key := "throttle:" + id
	now := time.Now()
	winStart := now.Add(-window).UnixNano()

	// Sliding-window log: drop expired members, add this hit, count the window.
	pipe := t.rdb.Pipeline()
	pipe.ZRemRangeByScore(ctx, key, "0", strconv.FormatInt(winStart, 10))
	member := fmt.Sprintf("%d-%d", now.UnixNano(), rand.Int64()) //nolint:gosec // member uniqueness, not security
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now.UnixNano()), Member: member})
	countCmd := pipe.ZCard(ctx, key)
	pipe.Expire(ctx, key, window)

	if _, err := pipe.Exec(ctx); err != nil {
		// Valkey down: fall back to the in-memory limiter.
		return t.mem.Check(ctx, id, limit, window)
	}

	count := int(countCmd.Val())
	return Result{
		Allowed:   count <= limit,
		Limit:     limit,
		Remaining: max(0, limit-count),
		Reset:     now.Add(window).Unix(),
	}, nil
}
