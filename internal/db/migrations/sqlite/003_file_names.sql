-- +goose Up
-- Original filename, e.g. "Quarterly report.pdf". Empty when not set.
ALTER TABLE files ADD COLUMN name TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 255);

-- +goose Down
ALTER TABLE files DROP COLUMN name;
