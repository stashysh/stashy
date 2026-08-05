// One-off backfill: imports legacy object-side file metadata into the files
// table, for deployments that predate the table.
//
// Before the files table, metadata lived with the stored bytes: GCS/S3 user
// metadata (owner, public, slug) plus ContentType/Size, or a {id}.meta JSON
// file next to {id}.data for the local backend. This script scans the
// configured backend and inserts a row for every file that doesn't already
// have one. Existing rows are never touched, so it is safe to re-run.
//
// Usage (backend and credentials come from the same env vars as the server):
//
//	STORAGE_BACKEND=gcs   GCS_BUCKET=my-bucket        DATABASE_URL=... go run ./scripts/backfill -dry-run
//	STORAGE_BACKEND=s3    S3_BUCKET=my-bucket         DATABASE_URL=... go run ./scripts/backfill
//	STORAGE_BACKEND=local LOCAL_STORAGE_DIR=./storage DATABASE_URL=... go run ./scripts/backfill
//
// Reads .env like the server. Run `stashy migrate` first so the files table
// exists. Delete this script once every deployment is backfilled.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	gcstorage "cloud.google.com/go/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"google.golang.org/api/iterator"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"
)

// legacyFile is one file's metadata as recovered from a storage backend.
type legacyFile struct {
	ID          string
	Owner       string
	ContentType string
	Size        int64
	Public      bool
	Slug        string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func main() {
	dryRun := flag.Bool("dry-run", false, "list what would be inserted without writing")
	flag.Parse()

	godotenv.Load()
	ctx := context.Background()

	db := openDB(ctx)
	defer db.Close()

	var inserted, present, unowned, failed int
	handle := func(f *legacyFile) {
		if f.Owner == "" {
			unowned++
			log.Printf("skip %s: no owner metadata", f.ID)
			return
		}
		if f.ContentType == "" {
			f.ContentType = "application/octet-stream"
		}
		if f.CreatedAt.IsZero() {
			f.CreatedAt = time.Now()
		}
		if f.UpdatedAt.IsZero() {
			f.UpdatedAt = f.CreatedAt
		}
		// File times are stored in UTC (see db.CreateFile).
		f.CreatedAt, f.UpdatedAt = f.CreatedAt.UTC(), f.UpdatedAt.UTC()

		if *dryRun {
			fmt.Printf("would insert %s owner=%s type=%s size=%d public=%t slug=%q created=%s\n",
				f.ID, f.Owner, f.ContentType, f.Size, f.Public, f.Slug, f.CreatedAt.Format(time.RFC3339))
			inserted++
			return
		}

		res, err := db.ExecContext(ctx, insertSQL(),
			f.ID, f.Owner, f.ContentType, f.Size, f.Public, f.Slug, f.CreatedAt, f.UpdatedAt)
		if err != nil {
			failed++
			log.Printf("insert %s: %v", f.ID, err)
			return
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		} else {
			present++
		}
	}

	backend := os.Getenv("STORAGE_BACKEND")
	var err error
	switch backend {
	case "gcs":
		err = scanGCS(ctx, requireEnv("GCS_BUCKET"), handle)
	case "s3":
		err = scanS3(ctx, requireEnv("S3_BUCKET"), handle)
	case "local":
		dir := os.Getenv("LOCAL_STORAGE_DIR")
		if dir == "" {
			dir = "./storage"
		}
		err = scanLocal(dir, handle)
	default:
		log.Fatalf("STORAGE_BACKEND=%q: nothing to backfill (set gcs, s3, or local)", backend)
	}
	if err != nil {
		log.Fatalf("scanning %s: %v", backend, err)
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

// scanGCS reads legacy metadata straight from the object listing; GCS returns
// user metadata inline, so this is one API call per ~1000 objects.
func scanGCS(ctx context.Context, bucket string, fn func(*legacyFile)) error {
	client, err := gcstorage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating GCS client: %w", err)
	}
	defer client.Close()

	it := client.Bucket(bucket).Objects(ctx, nil)
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("listing objects: %w", err)
		}
		fn(&legacyFile{
			ID:          attrs.Name,
			Owner:       attrs.Metadata["owner"],
			ContentType: attrs.ContentType,
			Size:        attrs.Size,
			Public:      attrs.Metadata["public"] == "true",
			Slug:        attrs.Metadata["slug"],
			CreatedAt:   attrs.Created,
			UpdatedAt:   attrs.Updated,
		})
	}
}

// scanS3 lists the bucket and HEADs each object — S3 listings don't include
// user metadata, so this costs one request per object.
func scanS3(ctx context.Context, bucket string, fn func(*legacyFile)) error {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}
	client := awss3.NewFromConfig(cfg)

	paginator := awss3.NewListObjectsV2Paginator(client, &awss3.ListObjectsV2Input{Bucket: &bucket})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("listing objects: %w", err)
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			head, err := client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &bucket, Key: &key})
			if err != nil {
				log.Printf("head %s: %v", key, err)
				continue
			}
			fn(&legacyFile{
				ID:          key,
				Owner:       head.Metadata["owner"],
				ContentType: aws.ToString(head.ContentType),
				Size:        aws.ToInt64(head.ContentLength),
				Public:      head.Metadata["public"] == "true",
				Slug:        head.Metadata["slug"],
				CreatedAt:   aws.ToTime(obj.LastModified),
				UpdatedAt:   aws.ToTime(obj.LastModified),
			})
		}
	}
	return nil
}

// scanLocal reads the legacy {id}.meta JSON files the local backend used to
// keep next to each {id}.data file.
func scanLocal(dir string, fn func(*legacyFile)) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading storage directory: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".meta") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".meta")

		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			log.Printf("read %s: %v", e.Name(), err)
			continue
		}
		var m struct {
			Owner       string `json:"owner"`
			ContentType string `json:"content_type"`
			Size        int64  `json:"size"`
			Public      bool   `json:"public"`
			Slug        string `json:"slug"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			log.Printf("decode %s: %v", e.Name(), err)
			continue
		}

		var created time.Time
		if info, err := os.Stat(filepath.Join(dir, id+".data")); err == nil {
			created = info.ModTime()
		} else if info, err := e.Info(); err == nil {
			created = info.ModTime()
		}

		fn(&legacyFile{
			ID:          id,
			Owner:       m.Owner,
			ContentType: m.ContentType,
			Size:        m.Size,
			Public:      m.Public,
			Slug:        m.Slug,
			CreatedAt:   created,
			UpdatedAt:   created,
		})
	}
	return nil
}

// Database plumbing

var usePostgres bool

func openDB(ctx context.Context) *sql.DB {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "file:stashy.db"
	}

	driver := "sqlite"
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		driver = "pgx"
		usePostgres = true
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		log.Fatalf("opening database: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("pinging database: %v", err)
	}
	return db
}

func insertSQL() string {
	if usePostgres {
		return `INSERT INTO files (id, owner_id, content_type, size, public, slug, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (id) DO NOTHING`
	}
	return `INSERT INTO files (id, owner_id, content_type, size, public, slug, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is required", key)
	}
	return v
}
