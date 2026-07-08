// One-off backfill: imports legacy object-side metadata from a GCS bucket
// into the files table, for deployments that predate the table.
//
// Before the files table, metadata lived on the GCS objects themselves
// (Metadata["owner"], ["public"], ["slug"], plus ContentType/Size). This
// script scans the bucket and inserts a row for every object that doesn't
// already have one. Existing rows are never touched, so it is safe to re-run.
//
// Usage:
//
//	GCS_BUCKET=my-bucket DATABASE_URL=postgres://... go run ./scripts/backfill-gcs -dry-run
//	GCS_BUCKET=my-bucket DATABASE_URL=postgres://... go run ./scripts/backfill-gcs
//
// Reads .env like the server. Uses Application Default Credentials for GCS.
// Delete this script once production is backfilled.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	gcstorage "cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "list what would be inserted without writing")
	flag.Parse()

	godotenv.Load()
	ctx := context.Background()

	bucketName := os.Getenv("GCS_BUCKET")
	if bucketName == "" {
		log.Fatal("GCS_BUCKET is required")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DB_DSN")
	}
	if dsn == "" {
		dsn = "file:stashy.db"
	}

	driver, insertSQL := "sqlite", insertSQLite
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		driver, insertSQL = "pgx", insertPostgres
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		log.Fatalf("opening database: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("pinging database: %v", err)
	}

	client, err := gcstorage.NewClient(ctx)
	if err != nil {
		log.Fatalf("creating GCS client: %v", err)
	}
	defer client.Close()

	var inserted, present, unowned, failed int
	it := client.Bucket(bucketName).Objects(ctx, nil)
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			log.Fatalf("listing objects: %v", err)
		}

		owner := attrs.Metadata["owner"]
		if owner == "" {
			unowned++
			log.Printf("skip %s: no owner metadata", attrs.Name)
			continue
		}

		contentType := attrs.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		public := attrs.Metadata["public"] == "true"
		slug := attrs.Metadata["slug"]
		createdAt := attrs.Created
		if createdAt.IsZero() {
			createdAt = time.Now()
		}
		updatedAt := attrs.Updated
		if updatedAt.IsZero() {
			updatedAt = createdAt
		}

		if *dryRun {
			fmt.Printf("would insert %s owner=%s type=%s size=%d public=%t slug=%q created=%s\n",
				attrs.Name, owner, contentType, attrs.Size, public, slug, createdAt.Format(time.RFC3339))
			inserted++
			continue
		}

		res, err := db.ExecContext(ctx, insertSQL,
			attrs.Name, owner, contentType, attrs.Size, public, slug, createdAt, updatedAt)
		if err != nil {
			failed++
			log.Printf("insert %s: %v", attrs.Name, err)
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		} else {
			present++
		}
	}

	verb := "inserted"
	if *dryRun {
		verb = "would insert"
	}
	log.Printf("backfill complete: %d %s, %d already present, %d skipped (no owner), %d failed",
		inserted, verb, present, unowned, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

const insertSQLite = `INSERT INTO files (id, owner_id, content_type, size, public, slug, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`

const insertPostgres = `INSERT INTO files (id, owner_id, content_type, size, public, slug, created_at, updated_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (id) DO NOTHING`
