// Package valkey builds and caches go-redis clients. An empty VALKEY_URL
// disables Valkey: Client returns (nil, nil) and every caller falls back to its
// in-memory implementation
package valkey

import (
	"context"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
)

var (
	mu      sync.Mutex
	url     string
	clients = map[string]*redis.Client{}
)

// Configure sets the Valkey URL once at startup (from cfg.ValkeyURL). Empty
// disables Valkey.
func Configure(valkeyURL string) { url = valkeyURL }

// Client returns a go-redis client for the alias, cached across calls. Returns
// (nil, nil) when Valkey is disabled — callers MUST handle a nil client and use
// their in-memory fallback.
func Client(alias string) (*redis.Client, error) {
	if url == "" {
		return nil, nil
	}

	mu.Lock()
	defer mu.Unlock()
	if c, ok := clients[alias]; ok {
		return c, nil
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("valkey: parse VALKEY_URL: %w", err)
	}
	c := redis.NewClient(opt)
	clients[alias] = c
	return c, nil
}

// Ping health-checks a client. A nil client (Valkey disabled) reports healthy so
// readiness does not fail when Valkey is intentionally off.
func Ping(ctx context.Context, c *redis.Client) error {
	if c == nil {
		return nil
	}
	return c.Ping(ctx).Err()
}

// Close shuts every cached client. Called from graceful shutdown
func Close() error {
	mu.Lock()
	defer mu.Unlock()
	var firstErr error
	for alias, c := range clients {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(clients, alias)
	}
	return firstErr
}
