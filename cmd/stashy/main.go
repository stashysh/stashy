package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	gcstorage "cloud.google.com/go/storage"
	"connectrpc.com/vanguard"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"

	"github.com/stashysh/stashy/gen/stashy/v1alpha1/stashyv1alpha1connect"
	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
	"github.com/stashysh/stashy/internal/service"
	"github.com/stashysh/stashy/internal/storage"
	"github.com/stashysh/stashy/internal/storage/gcs"
	"github.com/stashysh/stashy/internal/storage/local"
	"github.com/stashysh/stashy/internal/storage/memory"
	"github.com/stashysh/stashy/internal/web"
)

var Version = "dev"

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envRequired(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is required", key)
	}
	return v
}

func newStorage() (storage.Storage, error) {
	switch os.Getenv("STORAGE_BACKEND") {
	case "gcs":
		client, err := gcstorage.NewClient(context.Background())
		if err != nil {
			return nil, err
		}
		return gcs.New(client, envRequired("GCS_BUCKET")), nil
	case "local":
		return local.New(env("LOCAL_STORAGE_DIR", "./storage"))
	default:
		return memory.New(), nil
	}
}

func driverFromDSN(dsn string) string {
	switch {
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		return "pgx"
	case strings.HasPrefix(dsn, "mysql://"):
		return "mysql"
	default:
		return "sqlite"
	}
}

func openDB() (*db.DB, error) {
	dsn := env("DB_DSN", "file:stashy.db")
	return db.New(context.Background(), driverFromDSN(dsn), dsn)
}

func fileHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.NotFound(w, r)
			return
		}

		rc, meta, err := store.Get(r.Context(), id)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.NotFound(w, r)
				return
			}
			log.Printf("fileHandler: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer rc.Close()

		w.Header().Set("Content-Type", meta.ContentType)
		io.Copy(w, rc)
	}
}

var usage = "Usage: stashy " + Version + ` <command>

Commands:
  serve     Start the server (default)
  migrate   Run database migrations and exit
`

func main() {
	godotenv.Load()

	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "serve":
		cmdServe()
	case "migrate":
		cmdMigrate()
	case "version", "-v", "--version":
		fmt.Println("stashy " + Version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
}

func cmdMigrate() {
	database, err := openDB()
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer database.Close(context.Background())

	if err := database.Migrate(context.Background()); err != nil {
		log.Fatalf("migration failed: %v", err)
	}
	log.Println("migrations complete")
}

func cmdServe() {
	port := env("PORT", "8080")

	database, err := openDB()
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer database.Close(context.Background())

	store, err := newStorage()
	if err != nil {
		log.Fatalf("failed to create storage: %v", err)
	}

	sessions := auth.NewSessionManager(envRequired("SESSION_SECRET"))
	hostname := env("HOSTNAME", "http://localhost:"+port)

	oauth := auth.NewOAuthHandler(
		envRequired("GOOGLE_CLIENT_ID"),
		envRequired("GOOGLE_CLIENT_SECRET"),
		hostname+"/auth/google/callback",
		database,
		sessions,
	)

	apiKeys := auth.NewAPIKeyHandler(database, sessions)

	svc := service.New(store)
	path, handler := stashyv1alpha1connect.NewStorageServiceHandler(svc)

	transcoder, err := vanguard.NewTranscoder([]*vanguard.Service{
		vanguard.NewService(path, handler),
	})
	if err != nil {
		log.Fatalf("failed to create transcoder: %v", err)
	}

	apiAuth := auth.RequireAPIKey(database)
	webUI := web.NewHandler(database, sessions)

	mux := http.NewServeMux()

	oauth.RegisterRoutes(mux)
	apiKeys.RegisterRoutes(mux)

	mux.Handle("/v1/", apiAuth(transcoder))
	mux.Handle(path, apiAuth(transcoder))

	publicFS := http.FileServer(http.Dir("public"))
	if entries, err := os.ReadDir("public"); err == nil {
		for _, e := range entries {
			if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				mux.Handle("GET /"+e.Name(), publicFS)
			}
		}
	}

	mux.Handle("GET /{$}", webUI)
	mux.HandleFunc("GET /{id}", fileHandler(store))

	addr := ":" + port
	log.Printf("listening on %s", addr)

	if err := http.ListenAndServe(addr, h2c.NewHandler(mux, &http2.Server{})); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
