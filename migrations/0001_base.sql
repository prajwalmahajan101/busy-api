-- +goose Up
-- +goose StatementBegin
CREATE TABLE items (
    id         bigserial   PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    is_active  boolean     NOT NULL DEFAULT true,
    notes      jsonb
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_items_is_active ON items (is_active);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE items;
-- +goose StatementEnd
