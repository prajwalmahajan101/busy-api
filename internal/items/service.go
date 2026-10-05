// Package items is the CRUD domain module: HTTP handler → service → store.
// The service holds no HTTP concerns; the handler holds no SQL.
package items

import (
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

// Service is the business layer over the sqlc store. Every call runs under the
// DB query timeout
type Service struct {
	pool     *pgxpool.Pool
	q        *storedb.Queries
	cache    cache.Cache
	cacheTTL time.Duration
}

// NewService wires the store and a cache-aside backend for the single-item hot
// read. The cache is fail-open (a Valkey outage reports a miss), so a cache
// failure never surfaces as a 5xx — it just falls through to Postgres.
func NewService(pool *pgxpool.Pool, c cache.Cache, ttl time.Duration) *Service {
	return &Service{pool: pool, q: storedb.New(pool), cache: c, cacheTTL: ttl}
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
	return item, nil
}

func (s *Service) Get(ctx context.Context, id int64) (storedb.Item, error) {
	defer reqcontext.TrackService(ctx)()

	// Cache-aside: a hit serves the read without touching the DB pool — the
	// whole point of the rung, since the pool is the scarce resource under load.
	key := itemCacheKey(id)
	if b, hit, _ := s.cache.Get(ctx, key); hit {
		var item storedb.Item
		if json.Unmarshal(b, &item) == nil {
			return item, nil
		}
		// Corrupt entry: drop it and fall through to the DB.
		_ = s.cache.Delete(ctx, key)
	}

	ctx, cancel := db.WithQueryTimeout(ctx)
	defer cancel()

	stopRepo := reqcontext.TrackRepo(ctx)
	item, err := s.q.GetItem(ctx, id)
	stopRepo()
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedb.Item{}, errs.NewNotFound(msgItemNotFound)
		}
		return storedb.Item{}, errs.NewInfrastructure(msgGetFailed)
	}

	if b, err := json.Marshal(item); err == nil {
		_ = s.cache.Set(ctx, key, b, s.cacheTTL)
	}
	return item, nil
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
