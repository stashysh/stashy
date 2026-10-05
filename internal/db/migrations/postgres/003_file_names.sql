-- +goose Up
-- Original filename, e.g. "Quarterly report.pdf". Empty when not set.
ALTER TABLE files ADD COLUMN IF NOT EXISTS name VARCHAR(255) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE files DROP COLUMN IF EXISTS name;
