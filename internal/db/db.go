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

// File is the metadata record for a stored file. The database is the source
// of truth for metadata; storage backends only hold the bytes.
type File struct {
	ID          string
	Owner       string
	ContentType string
	Size        int64
	Public      bool
	Slug        string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type DB struct {
	sql     *sql.DB
	dialect string
}

func New(ctx context.Context, driver, dsn string) (*DB, error) {
	sqlDB, err := sql.Open(driver, dsn)
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

// Migrate runs all pending database migrations for the connected dialect.
func (d *DB) Migrate(ctx context.Context) error {
	dir := "sqlite"
	if d.dialect == "pgx" {
		dir = "postgres"
	}

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect(d.dialect); err != nil {
		return fmt.Errorf("setting goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, d.sql, dir); err != nil {
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

const fileColumns = `id, owner_id, content_type, size, public, slug, created_at, updated_at`

func scanFile(row interface{ Scan(...any) error }) (*File, error) {
	var f File
	err := row.Scan(&f.ID, &f.Owner, &f.ContentType, &f.Size, &f.Public, &f.Slug, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (d *DB) CreateFile(ctx context.Context, id, owner, contentType string, size int64) (*File, error) {
	// File times are stored in UTC: SQLite compares timestamps as text, so a
	// stable zone (and no monotonic-clock suffix) keeps ordering and the
	// keyset cursor in ListFiles correct.
	now := time.Now().UTC()
	query := d.q(`INSERT INTO files (id, owner_id, content_type, size, public, slug, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '', ?, ?)`)
	if _, err := d.sql.ExecContext(ctx, query, id, owner, contentType, size, false, now, now); err != nil {
		return nil, fmt.Errorf("inserting file: %w", err)
	}
	return &File{
		ID:          id,
		Owner:       owner,
		ContentType: contentType,
		Size:        size,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func (d *DB) GetFile(ctx context.Context, id string) (*File, error) {
	query := d.q(`SELECT ` + fileColumns + ` FROM files WHERE id = ?`)
	f, err := scanFile(d.sql.QueryRowContext(ctx, query, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("file not found: %s", id)
		}
		return nil, fmt.Errorf("getting file: %w", err)
	}
	return f, nil
}

// ListFiles returns up to limit of owner's files, newest first. A non-empty
// afterID (paired with its row's afterCreatedAt) is a keyset cursor: only
// files strictly older than that row are returned.
func (d *DB) ListFiles(ctx context.Context, owner string, limit int, afterCreatedAt time.Time, afterID string) ([]File, error) {
	query := `SELECT ` + fileColumns + ` FROM files WHERE owner_id = ?`
	args := []any{owner}
	if afterID != "" {
		query += ` AND (created_at, id) < (?, ?)`
		args = append(args, afterCreatedAt.UTC(), afterID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := d.sql.QueryContext(ctx, d.q(query), args...)
	if err != nil {
		return nil, fmt.Errorf("listing files: %w", err)
	}
	defer rows.Close()

	var files []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning file: %w", err)
		}
		files = append(files, *f)
	}
	return files, rows.Err()
}

// CheckFileOwner reports whether the file exists and is owned by owner,
// with error messages matching the storage-layer conventions
// ("file not found", "permission denied").
func (d *DB) CheckFileOwner(ctx context.Context, id, owner string) error {
	query := d.q(`SELECT owner_id FROM files WHERE id = ?`)
	var got string
	err := d.sql.QueryRowContext(ctx, query, id).Scan(&got)
	if err == sql.ErrNoRows {
		return fmt.Errorf("file not found: %s", id)
	}
	if err != nil {
		return fmt.Errorf("getting file owner: %w", err)
	}
	if got != owner {
		return fmt.Errorf("permission denied")
	}
	return nil
}

// UpdateFileContent records new content metadata after a file's bytes are replaced.
func (d *DB) UpdateFileContent(ctx context.Context, id, owner, contentType string, size int64) error {
	if err := d.CheckFileOwner(ctx, id, owner); err != nil {
		return err
	}
	query := d.q(`UPDATE files SET content_type = ?, size = ?, updated_at = ? WHERE id = ?`)
	if _, err := d.sql.ExecContext(ctx, query, contentType, size, time.Now().UTC(), id); err != nil {
		return fmt.Errorf("updating file: %w", err)
	}
	return nil
}

func (d *DB) SetFilePublic(ctx context.Context, id, owner string, public bool) error {
	if err := d.CheckFileOwner(ctx, id, owner); err != nil {
		return err
	}
	query := d.q(`UPDATE files SET public = ?, updated_at = ? WHERE id = ?`)
	if _, err := d.sql.ExecContext(ctx, query, public, time.Now().UTC(), id); err != nil {
		return fmt.Errorf("updating file visibility: %w", err)
	}
	return nil
}

// SetFileSlug sets the file's slug, or clears it when slug is empty.
func (d *DB) SetFileSlug(ctx context.Context, id, owner, slug string) error {
	if err := d.CheckFileOwner(ctx, id, owner); err != nil {
		return err
	}
	query := d.q(`UPDATE files SET slug = ?, updated_at = ? WHERE id = ?`)
	if _, err := d.sql.ExecContext(ctx, query, slug, time.Now().UTC(), id); err != nil {
		return fmt.Errorf("updating file slug: %w", err)
	}
	return nil
}

func (d *DB) DeleteFile(ctx context.Context, id, owner string) error {
	if err := d.CheckFileOwner(ctx, id, owner); err != nil {
		return err
	}
	query := d.q(`DELETE FROM files WHERE id = ?`)
	if _, err := d.sql.ExecContext(ctx, query, id); err != nil {
		return fmt.Errorf("deleting file: %w", err)
	}
	return nil
}

// q rewrites ? placeholders to $1, $2, ... for postgres. SQLite uses ? natively.
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
