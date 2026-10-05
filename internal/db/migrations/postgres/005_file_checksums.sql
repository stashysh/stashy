-- +goose Up
-- CRC32C of the content, base64-encoded big-endian (as GCS and S3 report it).
-- Empty when unknown, e.g. for files uploaded before this column existed.
ALTER TABLE files ADD COLUMN checksum TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE files DROP COLUMN checksum;
