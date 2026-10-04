-- Rung 2 (indexes). Swap the low-value full is_active bool index for a partial
-- index on id filtered to active rows. Rationale from EXPLAIN at 100k rows:
--   * is_active = true is non-selective (~all rows), so a plain (is_active)
--     index is never chosen and only costs write overhead.
--   * The hot list read is `WHERE is_active = true ORDER BY id LIMIT/OFFSET`.
--     A partial index on (id) WHERE is_active = true serves that scan pre-filtered
--     and pre-ordered, and — as soft-deletes (F-4) accumulate — keeps the scan
--     from walking dead rows. GetItem (id = $1 AND is_active) uses it too.
-- Note: no index fixes the exact count(*) Seq Scan; that is addressed by
-- switching CountItems to a reltuples estimate (see internal/store/queries).

-- +goose Up
-- +goose StatementBegin
CREATE INDEX idx_items_active_id ON items (id) WHERE is_active = true;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX idx_items_is_active;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE INDEX idx_items_is_active ON items (is_active);
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX idx_items_active_id;
-- +goose StatementEnd
