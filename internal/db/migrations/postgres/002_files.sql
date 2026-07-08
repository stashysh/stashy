-- +goose Up
-- id is a nanoid: always exactly 21 characters. Changing the ID scheme
-- (storage.NewID) requires a migration here.
CREATE TABLE IF NOT EXISTS files (
    id VARCHAR(21) PRIMARY KEY CHECK (char_length(id) = 21),
    owner_id BIGINT NOT NULL REFERENCES users(id),
    content_type TEXT NOT NULL,
    size BIGINT NOT NULL,
    public BOOLEAN NOT NULL DEFAULT FALSE,
    slug VARCHAR(128) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

-- Composite index serves both the owner filter and the newest-first keyset
-- pagination in ListFiles.
CREATE INDEX IF NOT EXISTS idx_files_owner_created ON files(owner_id, created_at, id);

-- +goose Down
DROP TABLE IF EXISTS files;
