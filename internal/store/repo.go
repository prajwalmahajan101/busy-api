// Package store holds generic, swappable persistence helpers layered over the
// sqlc-generated Queries. No business logic — mechanical pagination,
// soft-delete, and safe-sort primitives reused across tables
package store

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
)

// RowQuerier is the subset of a pgx pool/conn needed for a single-row read.
// *pgxpool.Pool satisfies it.
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// CountItemsEstimate returns the approximate live row count of a table from
// planner statistics (reltuples) in O(1), avoiding a full count(*) Seq Scan on
// the list hot path. reltuples is a whole-table estimate maintained by
// ANALYZE/autovacuum and ignores row-level filters (e.g. is_active); it is
// accurate while soft-deletes are a small fraction of the table.
// ponytail: swap to a maintained counter (trigger/outbox) or a cached exact
// count only if exact list totals become a hard requirement.
func CountItemsEstimate(ctx context.Context, q RowQuerier, relname string) (int64, error) {
	var n int64
	err := q.QueryRow(ctx,
		`SELECT GREATEST(reltuples, 0)::bigint FROM pg_class WHERE relname = $1`,
		relname,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: estimate count: %w", err)
	}
	return n, nil
}

// MaxPageSize caps a single page so a huge size can't overflow the int32
// limit/offset the generated queries take. Handlers may cap lower (task 26).
const MaxPageSize = 1000

// CountFunc returns the total row count for a filtered set.
type CountFunc func(context.Context) (int64, error)

// ListFunc returns one page of rows for the given limit/offset.
type ListFunc[T any] func(ctx context.Context, limit, offset int32) ([]T, error)

// DeleteFunc soft- or hard-deletes a row by id.
type DeleteFunc func(ctx context.Context, id int64) error

// ListPaginated runs countFn + listFn for a 1-based page and size, returning
// the page items and total count. page/size clamp to >= 1 so bad input can
// never produce a negative offset (R3).
func ListPaginated[T any](ctx context.Context, countFn CountFunc, listFn ListFunc[T], page, size int) ([]T, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 1
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	// int64 math + explicit bound: (page-1)*size must fit int32 before the
	// generated ListFunc truncates it, else a large page silently wraps.
	offset64 := int64(page-1) * int64(size)
	if offset64 > math.MaxInt32 {
		return nil, 0, fmt.Errorf("store: page %d out of range", page)
	}
	total, err := countFn(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("store: count: %w", err)
	}
	limit := int32(size)      //nolint:gosec // size clamped to [1, MaxPageSize]
	offset := int32(offset64) //nolint:gosec // offset64 checked <= MaxInt32 above
	items, err := listFn(ctx, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list: %w", err)
	}
	return items, total, nil
}

// SoftDelete flips is_active=false via the injected deleteFn (a sqlc
// SoftDelete* method), keeping the repo swappable per table.
func SoftDelete(ctx context.Context, deleteFn DeleteFunc, id int64) error {
	if err := deleteFn(ctx, id); err != nil {
		return fmt.Errorf("store: soft delete id=%d: %w", id, err)
	}
	return nil
}

// SafeOrderColumn returns col if whitelisted, else def. Guards ORDER BY against
// injection when a query interpolates a caller-supplied sort column — sqlc
// cannot parameterize identifiers (NFR-SEC1).
func SafeOrderColumn(col string, allowed map[string]struct{}, def string) string {
	if _, ok := allowed[col]; ok {
		return col
	}
	return def
}
