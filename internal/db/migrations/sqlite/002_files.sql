-- +goose Up
-- id is a nanoid: always exactly 21 characters. Changing the ID scheme
-- (storage.NewID) requires a migration here.
CREATE TABLE IF NOT EXISTS files (
    id TEXT PRIMARY KEY CHECK (length(id) = 21),
    owner_id INTEGER NOT NULL REFERENCES users(id),
    content_type TEXT NOT NULL,
    size INTEGER NOT NULL,
    public BOOLEAN NOT NULL DEFAULT FALSE,
    slug TEXT NOT NULL DEFAULT '' CHECK (length(slug) <= 128),
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

-- Composite index serves both the owner filter and the newest-first keyset
-- pagination in ListFiles.
CREATE INDEX IF NOT EXISTS idx_files_owner_created ON files(owner_id, created_at, id);

-- +goose Down
DROP TABLE IF EXISTS files;
