package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/stashysh/stashy/internal/db/migrations"
)

type User struct {
	ID        string
	GoogleID  string
	Email     string
	Name      string
	CreatedAt time.Time
}

type APIKey struct {
	ID        string
	UserID    string
	KeyHash   string
	KeyPrefix string
	Label     string
	CreatedAt time.Time
}

type DB struct {
	sql     *sql.DB
	dialect string
}

func New(ctx context.Context, driver, dsn string) (*DB, error) {
	// MySQL DSN: strip mysql:// prefix, go-sql-driver expects user:pass@tcp(host)/db
	actualDSN := dsn
	if driver == "mysql" {
		actualDSN = strings.TrimPrefix(dsn, "mysql://")
	}

	sqlDB, err := sql.Open(driver, actualDSN)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	dialect := driver
	if strings.Contains(driver, "sqlite") {
		dialect = "sqlite3"
	}

	return &DB{sql: sqlDB, dialect: dialect}, nil
}

// Migrate runs all pending database migrations.
func (d *DB) Migrate(ctx context.Context) error {
	migrations.Dialect = d.dialect

	if err := goose.SetDialect(d.dialect); err != nil {
		return fmt.Errorf("setting goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, d.sql, "."); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	return nil
}

func (d *DB) Close(_ context.Context) error {
	return d.sql.Close()
}

func (d *DB) UpsertUser(ctx context.Context, googleID, email, name string) (*User, error) {
	now := time.Now()

	switch d.dialect {
	case "mysql":
		query := `INSERT INTO users (google_id, email, name, created_at)
			VALUES (?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE email = VALUES(email), name = VALUES(name)`
		_, err := d.sql.ExecContext(ctx, query, googleID, email, name, now)
		if err != nil {
			return nil, fmt.Errorf("upserting user: %w", err)
		}
		// Fetch the user back.
		var user User
		err = d.sql.QueryRowContext(ctx,
			`SELECT id, google_id, email, name, created_at FROM users WHERE google_id = ?`,
			googleID).Scan(&user.ID, &user.GoogleID, &user.Email, &user.Name, &user.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("fetching upserted user: %w", err)
		}
		return &user, nil

	case "pgx":
		query := `INSERT INTO users (google_id, email, name, created_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT(google_id) DO UPDATE SET email = EXCLUDED.email, name = EXCLUDED.name
			RETURNING id, google_id, email, name, created_at`
		var user User
		err := d.sql.QueryRowContext(ctx, query, googleID, email, name, now).
			Scan(&user.ID, &user.GoogleID, &user.Email, &user.Name, &user.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("upserting user: %w", err)
		}
		return &user, nil

	default: // sqlite3
		query := `INSERT INTO users (google_id, email, name, created_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(google_id) DO UPDATE SET email = excluded.email, name = excluded.name
			RETURNING id, google_id, email, name, created_at`
		var user User
		err := d.sql.QueryRowContext(ctx, query, googleID, email, name, now).
			Scan(&user.ID, &user.GoogleID, &user.Email, &user.Name, &user.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("upserting user: %w", err)
		}
		return &user, nil
	}
}

func (d *DB) GetUserByID(ctx context.Context, id string) (*User, error) {
	query := d.q(`SELECT id, google_id, email, name, created_at FROM users WHERE id = ?`)

	var user User
	err := d.sql.QueryRowContext(ctx, query, id).
		Scan(&user.ID, &user.GoogleID, &user.Email, &user.Name, &user.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("getting user: %w", err)
	}
	return &user, nil
}

func (d *DB) CreateAPIKey(ctx context.Context, userID, label string) (string, *APIKey, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generating key: %w", err)
	}

	plaintext := base64.URLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(plaintext))
	keyHash := base64.URLEncoding.EncodeToString(hash[:])
	keyPrefix := plaintext[:8]
	now := time.Now()

	var id string
	switch d.dialect {
	case "mysql":
		result, err := d.sql.ExecContext(ctx,
			`INSERT INTO api_keys (user_id, key_hash, key_prefix, label, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			userID, keyHash, keyPrefix, label, now)
		if err != nil {
			return "", nil, fmt.Errorf("inserting api key: %w", err)
		}
		lastID, _ := result.LastInsertId()
		id = fmt.Sprintf("%d", lastID)

	default: // sqlite3, pgx
		query := d.q(`INSERT INTO api_keys (user_id, key_hash, key_prefix, label, created_at)
			VALUES (?, ?, ?, ?, ?) RETURNING id`)
		err := d.sql.QueryRowContext(ctx, query, userID, keyHash, keyPrefix, label, now).Scan(&id)
		if err != nil {
			return "", nil, fmt.Errorf("inserting api key: %w", err)
		}
	}

	key := &APIKey{
		ID:        id,
		UserID:    userID,
		KeyHash:   keyHash,
		KeyPrefix: keyPrefix,
		Label:     label,
		CreatedAt: now,
	}
	return plaintext, key, nil
}

func (d *DB) LookupAPIKey(ctx context.Context, plaintext string) (*APIKey, error) {
	hash := sha256.Sum256([]byte(plaintext))
	keyHash := base64.URLEncoding.EncodeToString(hash[:])

	query := d.q(`SELECT id, user_id, key_hash, key_prefix, label, created_at
		FROM api_keys WHERE key_hash = ?`)

	var key APIKey
	err := d.sql.QueryRowContext(ctx, query, keyHash).
		Scan(&key.ID, &key.UserID, &key.KeyHash, &key.KeyPrefix, &key.Label, &key.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("api key not found")
		}
		return nil, fmt.Errorf("looking up api key: %w", err)
	}
	return &key, nil
}

func (d *DB) ListAPIKeys(ctx context.Context, userID string) ([]APIKey, error) {
	query := d.q(`SELECT id, user_id, key_hash, key_prefix, label, created_at
		FROM api_keys WHERE user_id = ? ORDER BY created_at DESC`)

	rows, err := d.sql.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("listing api keys: %w", err)
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.KeyHash, &k.KeyPrefix, &k.Label, &k.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning api key: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (d *DB) DeleteAPIKey(ctx context.Context, keyID, userID string) error {
	query := d.q(`DELETE FROM api_keys WHERE id = ? AND user_id = ?`)

	result, err := d.sql.ExecContext(ctx, query, keyID, userID)
	if err != nil {
		return fmt.Errorf("deleting api key: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("api key not found")
	}
	return nil
}

// q rewrites ? placeholders to $1, $2, ... for postgres. MySQL and SQLite use ? natively.
func (d *DB) q(query string) string {
	if d.dialect != "pgx" {
		return query
	}
	var b strings.Builder
	n := 1
	for _, c := range query {
		if c == '?' {
			fmt.Fprintf(&b, "$%d", n)
			n++
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}
