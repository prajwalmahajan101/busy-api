// Package items is the CRUD domain module: HTTP handler → service → store.
// The service holds no HTTP concerns; the handler holds no SQL.
package items

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prajwalmahajan101/busyapi/internal/db"
	"github.com/prajwalmahajan101/busyapi/internal/errs"
	"github.com/prajwalmahajan101/busyapi/internal/reqcontext"
	"github.com/prajwalmahajan101/busyapi/internal/resilience/cache"
	"github.com/prajwalmahajan101/busyapi/internal/store"
	storedb "github.com/prajwalmahajan101/busyapi/internal/store/db"
	"github.com/redis/go-redis/v9"
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

// itemsVerKey is the raw Valkey key holding the list-cache version counter. It is
// read/written directly on rdb (INCR/GET), NOT through cache.Cache: the tiered
// cache fronts L2 with a fixed-TTL L1 (tiered.go), so a version read through the
// cache would be stale for up to the L1 TTL after a bump — defeating invalidation.
const itemsVerKey = "items:listver"

// Service is the business layer over the sqlc store. Every call runs under the
// DB query timeout
type Service struct {
	q        *storedb.Queries
	rdb      *redis.Client // raw client for the list-cache version counter; nil ⇒ atomic fallback
	ver      atomic.Int64  // in-process version, used when rdb is nil (single-instance)
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
// disables it. rdb is the raw Valkey client used only for the list-cache version
// counter (nil ⇒ in-process atomic fallback).
func NewService(pool *pgxpool.Pool, rdb *redis.Client, c cache.Cache, ttl, negTTL time.Duration, presence *Presence) *Service {
	return &Service{q: storedb.New(pool), rdb: rdb, cache: c, cacheTTL: ttl, negTTL: negTTL, presence: presence}
}

func itemCacheKey(id int64) string { return fmt.Sprintf("item:%d", id) }
func listCacheKey(ver int64, page, size int) string {
	return fmt.Sprintf("items:list:%d:%d:%d", ver, page, size)
}

// listVersion returns the current list-cache version. Reads INCR-maintained
// counter from Valkey (bypassing the cache tiers so a bump is seen immediately);
// on a nil client or any error it falls back to the in-process counter. Fully
// fail-open — a version read never fails a List.
func (s *Service) listVersion(ctx context.Context) int64 {
	if s.rdb == nil {
		return s.ver.Load()
	}
	v, err := s.rdb.Get(ctx, itemsVerKey).Int64()
	if err != nil {
		return s.ver.Load() // miss (key absent) or outage ⇒ sentinel; List still serves
	}
	return v
}

// bumpVersion advances the list-cache version so every pre-write list key orphans
// (they expire by TTL — no scan/delete). INCR on Valkey when present, else the
// in-process counter. Fail-open: a bump failure never fails the write.
//
// ponytail: cross-instance invalidation needs Valkey — with rdb nil the atomic is
// local, so a write on one instance will not invalidate another's list cache.
func (s *Service) bumpVersion(ctx context.Context) {
	if s.rdb == nil {
		s.ver.Add(1)
		return
	}
	if err := s.rdb.Incr(ctx, itemsVerKey).Err(); err != nil {
		s.ver.Add(1) // outage ⇒ advance the local counter so this instance still invalidates
	}
}

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
	// Invalidate cached list pages so the new item appears immediately (the old
	// single-item key is unaffected — this is a fresh id).
	s.bumpVersion(ctx)
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
		qctx, cancel := db.WithQueryTimeout(ctx)
		defer cancel()

		stopRepo := reqcontext.TrackRepo(qctx)
		item, err := s.q.GetItem(qctx, id)
		stopRepo()
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				_ = s.cache.Set(qctx, key, tombstone, s.negTTL)
			}
			return storedb.Item{}, err
		}

		if b, err := json.Marshal(item); err == nil {
			_ = s.cache.Set(qctx, key, b, s.cacheTTL)
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

type listResult struct {
	Items []storedb.Item `json:"items"`
	Total int64          `json:"total"`
}

func (s *Service) List(ctx context.Context, page, size int) ([]storedb.Item, int64, error) {
	defer reqcontext.TrackService(ctx)()

	ver := s.listVersion(ctx)
	key := listCacheKey(ver, page, size)
	if b, hit, _ := s.cache.Get(ctx, key); hit {
		var lr listResult
		if json.Unmarshal(b, &lr) == nil {
			return lr.Items, lr.Total, nil
		}
		_ = s.cache.Delete(ctx, key)
	}

	lr, err := s.loadAndCache(ctx, key, page, size)
	if err != nil {
		return nil, 0, errs.NewInfrastructure(msgListFailed)
	}
	return lr.Items, lr.Total, nil
}

// loadAndCache recomputes one list page from the DB (count + rows), records it
// under key, and returns it. Concurrent callers for the same key collapse via
// singleflight so a boundary miss burst is one DB read, not M (T26). The TTL
// avalanche (whole key set expiring together) is handled by L2 TTL jitter
// (CACHE_TTL_JITTER_PCT), not XFetch — see benchmark-logbook rung-5 note.
func (s *Service) loadAndCache(ctx context.Context, key string, page, size int) (listResult, error) {
	v, err, _ := s.sf.Do(key, func() (any, error) {
		qctx, cancel := db.WithQueryTimeout(ctx)
		defer cancel()

		listFn := func(ctx context.Context, limit, offset int32) ([]storedb.Item, error) {
			return s.q.ListItems(ctx, storedb.ListItemsParams{Limit: limit, Offset: offset})
		}
		countFn := func(ctx context.Context) (int64, error) {
			return s.q.CountItems(ctx)
		}
		stopRepo := reqcontext.TrackRepo(qctx)
		items, total, err := store.ListPaginated(qctx, countFn, listFn, page, size)
		stopRepo()
		if err != nil {
			return listResult{}, err
		}

		lr := listResult{Items: items, Total: total}
		if b, err := json.Marshal(lr); err == nil {
			_ = s.cache.Set(qctx, key, b, s.cacheTTL)
		}
		return lr, nil
	})
	if err != nil {
		return listResult{}, err
	}
	return v.(listResult), nil
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
	s.bumpVersion(ctx) // drop the item from cached list pages immediately
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
	s.bumpVersion(ctx) // drop the item from cached list pages immediately
	return nil
}
