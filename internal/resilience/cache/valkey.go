package cache

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

type valkeyCache struct {
	rdb    *redis.Client
	prefix string
}

func newValkeyCache(name string, rdb *redis.Client) *valkeyCache {
	return &valkeyCache{rdb: rdb, prefix: "cache:" + name + ":"}
}

func (c *valkeyCache) key(k string) string {
	return c.prefix + k
}

func (c *valkeyCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	val, err := c.rdb.Get(ctx, c.key(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil // genuine miss
	}
	if err != nil {
		slog.Warn("cache: valkey get failed, failing open", "key", key, "err", err)
		return nil, false, nil // fail-open as a miss
	}
	return val, true, nil
}

func (c *valkeyCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if err := c.rdb.Set(ctx, c.key(key), val, ttl).Err(); err != nil {
		slog.Warn("cache: valkey set failed, ignoring", "key", key, "err", err)
	}
	return nil // never propagate
}

func (c *valkeyCache) Delete(ctx context.Context, key string) error {
	if err := c.rdb.Del(ctx, c.key(key)).Err(); err != nil {
		slog.Warn("cache: valkey delete failed, ignoring", "key", key, "err", err)
	}
	return nil
}
