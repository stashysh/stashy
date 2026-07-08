-- +goose Up
CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    google_id TEXT NOT NULL UNIQUE,
    email TEXT NOT NULL,
    name TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS api_keys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id),
    key_hash TEXT NOT NULL UNIQUE,
    key_prefix TEXT NOT NULL,
    label TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_api_keys_user_id ON api_keys(user_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys(key_hash);

-- +goose Down
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS users;
