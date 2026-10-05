package gcs

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/option"
	gstorage "google.golang.org/api/storage/v1"

	"github.com/stashysh/stashy/internal/storage"
)

// fakeGCS implements the slice of the JSON API that Storage uses:
// multipart uploads, media downloads, and deletes.
type fakeGCS struct {
	mu      sync.Mutex
	objects map[string][]byte
	// failures is how many requests to answer with 503 before serving.
	failures int
}

func (f *fakeGCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failures > 0 {
		f.failures--
		http.Error(w, "try again", http.StatusServiceUnavailable)
		return
	}

	const prefix = "/storage/v1/b/bucket/o/"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/upload/storage/v1/b/bucket/o":
		obj, data, err := readMultipart(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.objects[obj.Name] = data
		h := storage.NewCRC32C()
		h.Write(data)
		obj.Size = uint64(len(data))
		obj.Crc32c = storage.EncodeCRC32C(h.Sum32())
		json.NewEncoder(w).Encode(obj)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, prefix) && r.URL.Query().Get("alt") == "media":
		data, ok := f.objects[strings.TrimPrefix(r.URL.Path, prefix)]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, prefix):
		id := strings.TrimPrefix(r.URL.Path, prefix)
		if _, ok := f.objects[id]; !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		delete(f.objects, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected request", http.StatusNotImplemented)
	}
}

// readMultipart splits a multipart upload into its metadata and media parts.
func readMultipart(r *http.Request) (*gstorage.Object, []byte, error) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, nil, err
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	meta, err := mr.NextPart()
	if err != nil {
		return nil, nil, err
	}
	var obj gstorage.Object
	if err := json.NewDecoder(meta).Decode(&obj); err != nil {
		return nil, nil, err
	}
	media, err := mr.NextPart()
	if err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(media)
	return &obj, data, err
}

func newTestStorage(t *testing.T) (*Storage, *fakeGCS) {
	t.Helper()

	initialBackoff = time.Millisecond
	fake := &fakeGCS{objects: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	service, err := gstorage.NewService(t.Context(),
		option.WithEndpoint(srv.URL+"/storage/v1/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return New(service, "bucket"), fake
}

func readAll(t *testing.T, rc io.ReadCloser) string {
	t.Helper()

	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	return string(b)
}

func TestStorage(t *testing.T) {
	s, _ := newTestStorage(t)
	ctx := t.Context()

	stored, err := s.Put(ctx, "file1", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// The CRC32C of "hello" as GCS reports it.
	if stored.Size != 5 || stored.Checksum != "mnG7TA==" {
		t.Errorf("Put = %+v, want size 5 and checksum mnG7TA==", stored)
	}

	rc, err := s.Get(ctx, "file1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := readAll(t, rc); got != "hello" {
		t.Errorf("Get = %q, want hello", got)
	}

	rc, err = s.GetRange(ctx, "file1", 1, 3)
	if err != nil {
		t.Fatalf("GetRange: %v", err)
	}
	if got := readAll(t, rc); got != "ell" {
		t.Errorf("GetRange(1, 3) = %q, want ell", got)
	}

	if err := s.Delete(ctx, "file1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "file1"); err == nil || !strings.Contains(err.Error(), "file not found") {
		t.Errorf("Get after Delete: err = %v, want file not found", err)
	}
	if err := s.Delete(ctx, "file1"); err != nil {
		t.Errorf("Delete of a missing file: %v", err)
	}
}

func TestStorageRetriesTransientErrors(t *testing.T) {
	s, fake := newTestStorage(t)
	ctx := t.Context()

	fake.failures = 1
	if _, err := s.Put(ctx, "file1", "text/plain", strings.NewReader("hello")); err != nil {
		t.Fatalf("Put after a 503: %v", err)
	}

	fake.failures = maxAttempts - 1
	rc, err := s.Get(ctx, "file1")
	if err != nil {
		t.Fatalf("Get after %d 503s: %v", maxAttempts-1, err)
	}
	readAll(t, rc)

	fake.failures = maxAttempts
	if _, err := s.Get(ctx, "file1"); err == nil {
		t.Errorf("Get after %d 503s: want an error", maxAttempts)
	}

	fake.failures = 1
	if err := s.Delete(ctx, "file1"); err != nil {
		t.Fatalf("Delete after a 503: %v", err)
	}
}
