-- +goose Up
-- Who can open a file by its URL: 'private' (only the owner), 'internal' (any
-- signed-in user), or 'public' (anyone). Replaces the public flag; files that
-- weren't public were already open to signed-in users, so they become
-- 'internal'.
ALTER TABLE files ADD COLUMN visibility TEXT NOT NULL DEFAULT 'internal'
    CHECK (visibility IN ('private', 'internal', 'public'));
UPDATE files SET visibility = CASE WHEN public THEN 'public' ELSE 'internal' END;
ALTER TABLE files DROP COLUMN public;

-- +goose Down
-- 'private' files become non-public, i.e. open to signed-in users again.
ALTER TABLE files ADD COLUMN public BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE files SET public = (visibility = 'public');
ALTER TABLE files DROP COLUMN visibility;
