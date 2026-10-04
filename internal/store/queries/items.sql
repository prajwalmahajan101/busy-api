-- name: CreateItem :one
INSERT INTO items (notes)
VALUES ($1)
RETURNING *;

-- name: GetItem :one
SELECT * FROM items
WHERE id = $1 AND is_active = true;

-- name: ListItems :many
SELECT * FROM items
WHERE is_active = true
ORDER BY id
LIMIT $1 OFFSET $2;

-- name: CountItems :one
-- Exact count. NOT used on the list hot path — count(*) WHERE is_active=true
-- Seq-Scans the whole table (~all rows active) and no index fixes it, so the list
-- endpoint uses store.CountItemsEstimate (reltuples) instead. Kept for callers
-- that need an exact count off the hot path.
SELECT count(*) FROM items
WHERE is_active = true;

-- name: SoftDeleteItem :exec
UPDATE items
SET is_active = false, updated_at = now()
WHERE id = $1 AND is_active = true;

-- name: DeleteItem :exec
DELETE FROM items
WHERE id = $1;
