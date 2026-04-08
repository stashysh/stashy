package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

// Dialect is set by the db package before migrations run.
var Dialect string

func init() {
	goose.AddMigrationContext(up001, down001)
}

func up001(ctx context.Context, tx *sql.Tx) error {
	var usersSQL, keysSQL string

	switch Dialect {
	case "pgx", "postgres":
		usersSQL = `CREATE TABLE IF NOT EXISTS users (
			id BIGSERIAL PRIMARY KEY,
			google_id TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL,
			name TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		)`
		keysSQL = `CREATE TABLE IF NOT EXISTS api_keys (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users(id),
			key_hash TEXT NOT NULL UNIQUE,
			key_prefix TEXT NOT NULL,
			label TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		)`
	default: // sqlite3
		usersSQL = `CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			google_id TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL,
			name TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`
		keysSQL = `CREATE TABLE IF NOT EXISTS api_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL REFERENCES users(id),
			key_hash TEXT NOT NULL UNIQUE,
			key_prefix TEXT NOT NULL,
			label TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`
	}

	if _, err := tx.ExecContext(ctx, usersSQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, keysSQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_api_keys_user_id ON api_keys(user_id)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys(key_hash)`); err != nil {
		return err
	}
	return nil
}

func down001(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS api_keys`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS users`)
	return err
}
