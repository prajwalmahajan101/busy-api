// Package items is the CRUD domain module: HTTP handler → service → store.
// The service holds no HTTP concerns; the handler holds no SQL.
package items

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prajwalmahajan101/busyapi/internal/db"
	"github.com/prajwalmahajan101/busyapi/internal/errs"
	"github.com/prajwalmahajan101/busyapi/internal/reqcontext"
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
	pool *pgxpool.Pool
	q    *storedb.Queries
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, q: storedb.New(pool)}
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
	return item, nil
}

func (s *Service) Get(ctx context.Context, id int64) (storedb.Item, error) {
	defer reqcontext.TrackService(ctx)()
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
	return item, nil
}

func (s *Service) List(ctx context.Context, page, size int) ([]storedb.Item, int64, error) {
	defer reqcontext.TrackService(ctx)()
	ctx, cancel := db.WithQueryTimeout(ctx)
	defer cancel()
	listFn := func(ctx context.Context, limit, offest int32) ([]storedb.Item, error) {
		return s.q.ListItems(ctx, storedb.ListItemsParams{Limit: limit, Offset: offest})
	}
	stopRepo := reqcontext.TrackRepo(ctx)
	items, total, err := store.ListPaginated(ctx, s.q.CountItems, listFn, page, size)
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
	return nil
}
