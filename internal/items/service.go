// Package items is the CRUD domain module: HTTP handler → service → store.
// The service holds no HTTP concerns; the handler holds no SQL.
package items

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prajwalmahajan101/busyapi/internal/db"
	"github.com/prajwalmahajan101/busyapi/internal/errs"
	"github.com/prajwalmahajan101/busyapi/internal/reqcontext"
	"github.com/prajwalmahajan101/busyapi/internal/resilience/cache"
	"github.com/prajwalmahajan101/busyapi/internal/store"
	storedb "github.com/prajwalmahajan101/busyapi/internal/store/db"
	"golang.org/x/sync/singleflight"
)

// Error messages emitted by this package, named so handler and service share one
// source and tests can assert on them.
const (
	msgCreateFailed     = "create item failed"
	msgItemNotFound     = "item not found"
	msgGetFailed        = "get item failed"
	msgListFailed       = "list item failed"
	msgSoftDeleteFailed = "soft delete failed"
	msgDeleteFailed     = "delete item failed"
	msgInvalidBody      = "invalid request body"
	msgInvalidID        = "invalid id"
)

// Success messages emitted by the handler on 2xx responses.
const (
	msgItemCreated     = "item created"
	msgItemFetched     = "ok"
	msgItemSoftDeleted = "item soft deleted"
	msgItemDeleted     = "item deleted"
)

// Query and path parameter keys owned by this package's routes.
const (
	qpPage = "page"
	qpSize = "size"
	ppID   = "id"
)

// tombstone is the negative-cache sentinel: a confirmed-absent id is cached as
// this value so a repeated bad id (one that passed the bloom as a false positive)
// is answered from cache, not the DB (cache penetration, T28). The leading 0x00
// byte cannot start a marshalled storedb.Item (a JSON object begins with '{'), so
// a tombstone is unambiguously distinguishable from a real cached value.
var tombstone = []byte{0}

// Service is the business layer over the sqlc store. Every call runs under the
// DB query timeout
type Service struct {
	pool     *pgxpool.Pool
	q        *storedb.Queries
	cache    cache.Cache
	cacheTTL time.Duration
	negTTL   time.Duration      // negative-cache (tombstone) TTL for absent ids (T28)
	presence *Presence          // bloom of existing ids; nil = disabled (T28)
	sf       singleflight.Group // collapses concurrent same-key cache misses (T26)
}

// NewService wires the store and a cache-aside backend for the single-item hot
// read. The cache is fail-open (a Valkey outage reports a miss), so a cache
// failure never surfaces as a 5xx — it just falls through to Postgres. A non-nil
// presence filter short-circuits known-absent ids before any DB touch (T28); nil
// disables it.
func NewService(pool *pgxpool.Pool, c cache.Cache, ttl, negTTL time.Duration, presence *Presence) *Service {
	return &Service{pool: pool, q: storedb.New(pool), cache: c, cacheTTL: ttl, negTTL: negTTL, presence: presence}
}

func itemCacheKey(id int64) string { return fmt.Sprintf("item:%d", id) }

func (s *Service) Create(ctx context.Context, notes []byte) (storedb.Item, error) {
	defer reqcontext.TrackService(ctx)()
	ctx, cancel := db.WithQueryTimeout(ctx)
	defer cancel()

	stopRepo := reqcontext.TrackRepo(ctx)
	item, err := s.q.CreateItem(ctx, notes)
	stopRepo()
	if err != nil {
		return storedb.Item{}, errs.NewInfrastructure(msgCreateFailed)
	}
	// Record the new id in the bloom BEFORE returning: the caller only learns the
	// id from this response, so no external Get can race ahead of the Add and get
	// a false "absent" (T28).
	if s.presence != nil {
		s.presence.Add(item.ID)
	}
	return item, nil
}

func (s *Service) Get(ctx context.Context, id int64) (storedb.Item, error) {
	defer reqcontext.TrackService(ctx)()

	// Bloom pre-filter: a definitely-absent id is 404'd in-process, before any
	// cache or DB touch. This is the cache-penetration defence (T28) — a flood of
	// random absent ids can no longer amplify straight onto the DB.
	if s.presence != nil && !s.presence.MayExist(id) {
		return storedb.Item{}, errs.NewNotFound(msgItemNotFound)
	}

	// Cache-aside: a hit serves the read without touching the DB pool — the
	// whole point of the rung, since the pool is the scarce resource under load.
	key := itemCacheKey(id)
	if b, hit, _ := s.cache.Get(ctx, key); hit {
		// Negative-cache hit: a confirmed-absent id (a bloom false positive that
		// reached the DB once) is answered from cache, not re-read (T28).
		if bytes.Equal(b, tombstone) {
			return storedb.Item{}, errs.NewNotFound(msgItemNotFound)
		}
		var item storedb.Item
		if json.Unmarshal(b, &item) == nil {
			return item, nil
		}
		// Corrupt entry: drop it and fall through to the DB.
		_ = s.cache.Delete(ctx, key)
	}

	// singleflight: when a hot key expires, many requests miss at once; collapse
	// the concurrent misses so exactly one goroutine reads the DB + repopulates
	// and the rest share its result (cache stampede, T26). Keyed by cache key.
	//
	// ponytail: Do shares the leader's ctx — a cancelled leader fails its followers;
	// fine for a sub-ms PK read, revisit with DoChan if the loader grows slow.
	// ponytail: no in-closure cache re-check — singleflight already collapses the
	// concurrent burst and the leader's Set makes the next arrivals hit; add one
	// only if a post-leader micro-race shows up in the numbers.
	v, err, _ := s.sf.Do(key, func() (any, error) {
		ctx, cancel := db.WithQueryTimeout(ctx)
		defer cancel()

		stopRepo := reqcontext.TrackRepo(ctx)
		item, err := s.q.GetItem(ctx, id)
		stopRepo()
		if err != nil {
			// Negative cache: remember a confirmed-absent id briefly so a repeat
			// (a bloom false positive) is served from cache, not the DB (T28).
			if errors.Is(err, pgx.ErrNoRows) {
				_ = s.cache.Set(ctx, key, tombstone, s.negTTL)
			}
			return storedb.Item{}, err
		}

		if b, err := json.Marshal(item); err == nil {
			_ = s.cache.Set(ctx, key, b, s.cacheTTL)
		}
		return item, nil
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedb.Item{}, errs.NewNotFound(msgItemNotFound)
		}
		return storedb.Item{}, errs.NewInfrastructure(msgGetFailed)
	}
	return v.(storedb.Item), nil
}

func (s *Service) List(ctx context.Context, page, size int) ([]storedb.Item, int64, error) {
	defer reqcontext.TrackService(ctx)()
	ctx, cancel := db.WithQueryTimeout(ctx)
	defer cancel()
	listFn := func(ctx context.Context, limit, offest int32) ([]storedb.Item, error) {
		return s.q.ListItems(ctx, storedb.ListItemsParams{Limit: limit, Offset: offest})
	}
	// Approximate total from planner stats (O(1)) — exact count(*) Seq-Scans the
	// whole table and no index fixes it. See store.CountItemsEstimate.
	countFn := func(ctx context.Context) (int64, error) {
		return store.CountItemsEstimate(ctx, s.pool, "items")
	}
	stopRepo := reqcontext.TrackRepo(ctx)
	items, total, err := store.ListPaginated(ctx, countFn, listFn, page, size)
	stopRepo()
	if err != nil {
		return nil, 0, errs.NewInfrastructure(msgListFailed)
	}

	return items, total, nil
}

func (s *Service) SoftDelete(ctx context.Context, id int64) error {
	defer reqcontext.TrackService(ctx)()
	ctx, cancel := db.WithQueryTimeout(ctx)
	defer cancel()
	stopRepo := reqcontext.TrackRepo(ctx)
	err := store.SoftDelete(ctx, s.q.SoftDeleteItem, id)
	stopRepo()
	if err != nil {
		return errs.NewInfrastructure(msgSoftDeleteFailed)
	}
	_ = s.cache.Delete(ctx, itemCacheKey(id))
	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	defer reqcontext.TrackService(ctx)()
	ctx, cancel := db.WithQueryTimeout(ctx)
	defer cancel()

	stopRepo := reqcontext.TrackRepo(ctx)
	err := s.q.DeleteItem(ctx, id)
	stopRepo()
	if err != nil {
		return errs.NewInfrastructure(msgDeleteFailed)
	}
	_ = s.cache.Delete(ctx, itemCacheKey(id))
	return nil
}
