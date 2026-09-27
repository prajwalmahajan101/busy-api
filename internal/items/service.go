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
	"github.com/prajwalmahajan101/busyapi/internal/store"
	storedb "github.com/prajwalmahajan101/busyapi/internal/store/db"
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
	ctx, cancel := db.WithQueryTimeout(ctx)
	defer cancel()
	item, err := s.q.CreateItem(ctx, notes)
	if err != nil {
		return storedb.Item{}, errs.NewInfrastructure("create item failed")
	}
	return item, nil
}

func (s *Service) Get(ctx context.Context, id int64) (storedb.Item, error) {
	ctx, cancel := db.WithQueryTimeout(ctx)
	defer cancel()

	item, err := s.q.GetItem(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedb.Item{}, errs.NewNotFound("item not found")
		}
		return storedb.Item{}, errs.NewInfrastructure("get item failed")
	}
	return item, nil
}

func (s *Service) List(ctx context.Context, page, size int) ([]storedb.Item, int64, error) {
	ctx, cancel := db.WithQueryTimeout(ctx)
	defer cancel()
	listFn := func(ctx context.Context, limit, offest int32) ([]storedb.Item, error) {
		return s.q.ListItems(ctx, storedb.ListItemsParams{Limit: limit, Offset: offest})
	}
	items, total, err := store.ListPaginated(ctx, s.q.CountItems, listFn, page, size)
	if err != nil {
		return nil, 0, errs.NewInfrastructure("list item failed")
	}

	return items, total, nil
}

func (s *Service) SoftDelete(ctx context.Context, id int64) error {
	ctx, cancel := db.WithQueryTimeout(ctx)
	defer cancel()
	if err := store.SoftDelete(ctx, s.q.SoftDeleteItem, id); err != nil {
		return errs.NewInfrastructure("soft delete failed")
	}

	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	ctx, cancel := db.WithQueryTimeout(ctx)
	defer cancel()

	if err := s.q.DeleteItem(ctx, id); err != nil {
		return errs.NewInfrastructure("delete item failed")
	}
	return nil
}
