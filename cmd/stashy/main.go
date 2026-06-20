package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	gcstorage "cloud.google.com/go/storage"
	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"connectrpc.com/vanguard"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

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
	s3storage "github.com/stashysh/stashy/internal/storage/s3"
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
	case "s3":
		cfg, err := config.LoadDefaultConfig(context.Background())
		if err != nil {
			return nil, fmt.Errorf("loading AWS config: %w", err)
		}
		client := s3.NewFromConfig(cfg)
		return s3storage.New(client, envRequired("S3_BUCKET")), nil
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
	default:
		return "sqlite"
	}
}

func openDB() (*db.DB, error) {
	dsn := env("DATABASE_URL", env("DB_DSN", "file:stashy.db"))
	return db.New(context.Background(), driverFromDSN(dsn), dsn)
}

// fileHandler serves the public file namespace at the root: /{id} or
// /{id}/{slug}. It is registered as the catch-all so it doesn't conflict with
// the /v1/ API subtree, so it parses the path itself.
func fileHandler(store storage.Storage, service *service.StorageService, sessions *auth.SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		id, urlSlug, ok := splitFilePath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}

		meta, err := store.Stat(r.Context(), id)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.NotFound(w, r)
				return
			}
			log.Printf("fileHandler: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if !meta.Public {
			if _, ok := sessions.GetUserID(r); !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		// Redirect any non-matching slug (a bare /{id}, a stale slug from before
		// a rename, or a typo) to the current canonical URL, so links shared
		// across renames keep working.
		if urlSlug != meta.Slug {
			http.Redirect(w, r, canonicalPath(meta), http.StatusFound)
			return
		}

		service.ServeFile(w, r, id)
	}
}

// splitFilePath parses a root request path into a file id and optional slug.
// It reports false for anything that isn't /{id} or /{id}/{slug}.
func splitFilePath(p string) (id, slug string, ok bool) {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] != "":
		return parts[0], "", true
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return parts[0], parts[1], true
	default:
		return "", "", false
	}
}

// canonicalPath is the canonical access path for a file: /{id}/{slug}, or
// /{id} when it has no slug.
func canonicalPath(meta *storage.FileMeta) string {
	if meta.Slug != "" {
		return "/" + meta.ID + "/" + meta.Slug
	}
	return "/" + meta.ID
}

var usage = "Usage: stashy " + Version + ` <command>

Commands:
  serve [--migrate]   Start the server (default)
  migrate             Run database migrations and exit
`

func main() {
	godotenv.Load()

	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	migrate := false
	for _, arg := range os.Args[1:] {
		if arg == "--migrate" {
			migrate = true
		}
	}

	switch cmd {
	case "serve":
		cmdServe(migrate)
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

func cmdServe(migrate bool) {
	port := env("PORT", "8080")

	database, err := openDB()
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer database.Close(context.Background())

	if migrate {
		if err := database.Migrate(context.Background()); err != nil {
			log.Fatalf("migration failed: %v", err)
		}
	}

	store, err := newStorage()
	if err != nil {
		log.Fatalf("failed to create storage: %v", err)
	}

	sessions := auth.NewSessionManager(envRequired("SESSION_SECRET"))
	hostname := env("HOSTNAME", "http://localhost:"+port)

	var allowedDomains []string
	if v := os.Getenv("ALLOWED_DOMAINS"); v != "" {
		allowedDomains = strings.Split(v, ",")
	}

	oauth := auth.NewOAuthHandler(
		envRequired("GOOGLE_CLIENT_ID"),
		envRequired("GOOGLE_CLIENT_SECRET"),
		hostname+"/auth/google/callback",
		database,
		sessions,
		allowedDomains,
	)

	apiKeys := auth.NewAPIKeyHandler(database, sessions)

	svc := service.New(store, hostname)
	path, handler := stashyv1alpha1connect.NewStorageServiceHandler(svc, connect.WithInterceptors(validate.NewInterceptor()))

	restOpts := vanguard.WithRESTUnmarshalOptions(vanguard.RESTUnmarshalOptions{
		DiscardUnknownQueryParams: true,
	})

	transcoder, err := vanguard.NewTranscoder([]*vanguard.Service{
		vanguard.NewService(path, handler, restOpts),
	},
		vanguard.WithCodec(func(res vanguard.TypeResolver) vanguard.Codec {
			codec := vanguard.NewJSONCodec(res)
			codec.MarshalOptions.UseProtoNames = true
			codec.MarshalOptions.EmitUnpopulated = true
			codec.UnmarshalOptions.DiscardUnknown = true
			return codec
		}),
	)
	if err != nil {
		log.Fatalf("failed to create transcoder: %v", err)
	}

	apiAuth := auth.RequireAPIKey(database)
	webUI := web.NewHandler(database, sessions)

	mux := http.NewServeMux()

	oauth.RegisterRoutes(mux)
	apiKeys.RegisterRoutes(mux)

	// Register direct handlers for upload/download before Vanguard to bypass its
	// full-body buffering of HttpBody RPCs (see github.com/stashysh/stashy/issues/23).
	mux.Handle("POST /v1/files", apiAuth(http.HandlerFunc(svc.HTTPUpload)))
	mux.Handle("GET /v1/files/{id}", apiAuth(http.HandlerFunc(svc.HTTPDownload)))
	mux.Handle("PUT /v1/files/{id}", apiAuth(http.HandlerFunc(svc.HTTPReplace)))

	mux.Handle("/v1/", apiAuth(transcoder))
	mux.Handle(path, apiAuth(transcoder))

	publicFS := http.FileServer(http.Dir("public"))
	if spec, err := os.ReadFile("public/openapi.yaml"); err == nil {
		spec = bytes.ReplaceAll(spec, []byte("https://stashy.example.com"), []byte(hostname))
		mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
			w.Write(spec)
		})
	}
	if entries, err := os.ReadDir("public"); err == nil {
		for _, e := range entries {
			if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != "openapi.yaml" {
				mux.Handle("GET /"+e.Name(), publicFS)
			}
		}
	}

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /{$}", webUI)
	mux.Handle("/", fileHandler(store, svc, sessions))

	addr := ":" + port
	log.Printf("listening on %s", addr)

	if err := http.ListenAndServe(addr, h2c.NewHandler(mux, &http2.Server{})); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
