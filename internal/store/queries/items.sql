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
SELECT count(*) FROM items
WHERE is_active = true;

-- name: SoftDeleteItem :exec
UPDATE items
SET is_active = false, updated_at = now()
WHERE id = $1 AND is_active = true;

-- name: DeleteItem :exec
DELETE FROM items
WHERE id = $1;
