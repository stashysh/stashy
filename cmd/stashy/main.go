package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	gcstorage "cloud.google.com/go/storage"
	"connectrpc.com/vanguard"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/stashysh/stashy/gen/stashy/v1alpha1/stashyv1alpha1connect"
	"github.com/stashysh/stashy/internal/service"
	"github.com/stashysh/stashy/internal/storage"
	"github.com/stashysh/stashy/internal/storage/gcs"
	"github.com/stashysh/stashy/internal/storage/local"
	"github.com/stashysh/stashy/internal/storage/memory"
)

func newStorage() (storage.Storage, error) {
	switch os.Getenv("STORAGE_BACKEND") {
	case "gcs":
		client, err := gcstorage.NewClient(context.Background())
		if err != nil {
			return nil, err
		}
		bucket := os.Getenv("GCS_BUCKET")
		if bucket == "" {
			log.Fatal("GCS_BUCKET is required when STORAGE_BACKEND=gcs")
		}
		return gcs.New(client, bucket), nil
	case "local":
		dir := os.Getenv("LOCAL_STORAGE_DIR")
		if dir == "" {
			dir = "./storage"
		}
		return local.New(dir)
	default:
		return memory.New(), nil
	}
}

func rootHandler(store storage.Storage, public http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")

		// Try /{uuid} from storage first.
		if path != "" {
			rc, meta, err := store.Get(r.Context(), path)
			if err == nil {
				defer rc.Close()
				w.Header().Set("Content-Type", meta.ContentType)
				io.Copy(w, rc)
				return
			}
		}

		// Fall back to public/ static files.
		public.ServeHTTP(w, r)
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	store, err := newStorage()
	if err != nil {
		log.Fatalf("failed to create storage: %v", err)
	}

	svc := service.New(store)

	path, handler := stashyv1alpha1connect.NewStorageServiceHandler(svc)

	services := []*vanguard.Service{
		vanguard.NewService(path, handler),
	}

	transcoder, err := vanguard.NewTranscoder(services)
	if err != nil {
		log.Fatalf("failed to create transcoder: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/", transcoder)
	mux.Handle(path, transcoder)
	public := http.FileServer(http.Dir("public"))
	mux.HandleFunc("/", rootHandler(store, public))

	addr := ":" + port
	log.Printf("listening on %s", addr)

	if err := http.ListenAndServe(addr, h2c.NewHandler(mux, &http2.Server{})); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
