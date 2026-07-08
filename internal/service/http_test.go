package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stashysh/stashy/internal/db"
	"github.com/stashysh/stashy/internal/storage"
	"github.com/stashysh/stashy/internal/storage/memory"
)

func TestServeFileFullResponse(t *testing.T) {
	svc, id := newTestService(t, memory.New(), "video/mp4", "0123456789")

	req := httptest.NewRequest(http.MethodGet, "/"+id, nil)
	rec := httptest.NewRecorder()

	svc.ServeFile(rec, req, id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("Content-Type = %q, want video/mp4", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "10" {
		t.Fatalf("Content-Length = %q, want 10", got)
	}
	if got := rec.Body.String(); got != "0123456789" {
		t.Fatalf("body = %q", got)
	}
}

func TestServeFileByteRange(t *testing.T) {
	store := &rangeTrackingStore{Storage: memory.New()}
	svc, id := newTestService(t, store, "video/mp4", "0123456789")

	req := httptest.NewRequest(http.MethodGet, "/"+id, nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()

	svc.ServeFile(rec, req, id)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusPartialContent)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q, want bytes 2-5/10", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "4" {
		t.Fatalf("Content-Length = %q, want 4", got)
	}
	if got := rec.Body.String(); got != "2345" {
		t.Fatalf("body = %q", got)
	}
	if store.getCalls != 0 {
		t.Fatalf("Get calls = %d, want 0", store.getCalls)
	}
	if store.getRangeCalls != 1 {
		t.Fatalf("GetRange calls = %d, want 1", store.getRangeCalls)
	}
	if store.rangeStart != 2 || store.rangeLength != 4 {
		t.Fatalf("range = %d+%d, want 2+4", store.rangeStart, store.rangeLength)
	}
}

func TestServeFileHeadDoesNotOpenBody(t *testing.T) {
	store := &rangeTrackingStore{Storage: memory.New()}
	svc, id := newTestService(t, store, "video/mp4", "0123456789")

	req := httptest.NewRequest(http.MethodHead, "/"+id, nil)
	rec := httptest.NewRecorder()

	svc.ServeFile(rec, req, id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Length"); got != "10" {
		t.Fatalf("Content-Length = %q, want 10", got)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
	if store.getCalls != 0 {
		t.Fatalf("Get calls = %d, want 0", store.getCalls)
	}
	if store.getRangeCalls != 0 {
		t.Fatalf("GetRange calls = %d, want 0", store.getRangeCalls)
	}
}

func TestServeFileHeadByteRangeDoesNotOpenRangeBody(t *testing.T) {
	store := &rangeTrackingStore{Storage: memory.New()}
	svc, id := newTestService(t, store, "video/mp4", "0123456789")

	req := httptest.NewRequest(http.MethodHead, "/"+id, nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()

	svc.ServeFile(rec, req, id)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusPartialContent)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q, want bytes 2-5/10", got)
	}
	if store.getRangeCalls != 0 {
		t.Fatalf("GetRange calls = %d, want 0", store.getRangeCalls)
	}
}

func TestServeFileOpenEndedByteRange(t *testing.T) {
	svc, id := newTestService(t, memory.New(), "video/mp4", "0123456789")

	req := httptest.NewRequest(http.MethodGet, "/"+id, nil)
	req.Header.Set("Range", "bytes=7-")
	rec := httptest.NewRecorder()

	svc.ServeFile(rec, req, id)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusPartialContent)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 7-9/10" {
		t.Fatalf("Content-Range = %q, want bytes 7-9/10", got)
	}
	if got := rec.Body.String(); got != "789" {
		t.Fatalf("body = %q", got)
	}
}

func TestServeFileSuffixByteRange(t *testing.T) {
	svc, id := newTestService(t, memory.New(), "video/mp4", "0123456789")

	req := httptest.NewRequest(http.MethodGet, "/"+id, nil)
	req.Header.Set("Range", "bytes=-4")
	rec := httptest.NewRecorder()

	svc.ServeFile(rec, req, id)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusPartialContent)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 6-9/10" {
		t.Fatalf("Content-Range = %q, want bytes 6-9/10", got)
	}
	if got := rec.Body.String(); got != "6789" {
		t.Fatalf("body = %q", got)
	}
}

func TestServeFileUnsatisfiableByteRange(t *testing.T) {
	svc, id := newTestService(t, memory.New(), "video/mp4", "0123456789")

	req := httptest.NewRequest(http.MethodGet, "/"+id, nil)
	req.Header.Set("Range", "bytes=10-20")
	rec := httptest.NewRecorder()

	svc.ServeFile(rec, req, id)

	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestedRangeNotSatisfiable)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes */10" {
		t.Fatalf("Content-Range = %q, want bytes */10", got)
	}
}

func newTestDB(t *testing.T) *db.DB {
	t.Helper()

	database, err := db.New(t.Context(), "sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { database.Close(context.Background()) })

	if err := database.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return database
}

// newTestService builds a StorageService over store with one stored file and
// returns the service and the file's id.
func newTestService(t *testing.T, store storage.Storage, contentType, body string) (*StorageService, string) {
	t.Helper()

	database := newTestDB(t)

	id, err := storage.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, err := store.Put(t.Context(), id, strings.NewReader(body)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := database.CreateFile(t.Context(), id, "1", contentType, int64(len(body))); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}

	return New(store, database, "http://example.test"), id
}

type rangeTrackingStore struct {
	storage.Storage
	getCalls      int
	getRangeCalls int
	rangeStart    int64
	rangeLength   int64
}

func (s *rangeTrackingStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	s.getCalls++
	return s.Storage.Get(ctx, id)
}

func (s *rangeTrackingStore) GetRange(ctx context.Context, id string, start, length int64) (io.ReadCloser, error) {
	s.getRangeCalls++
	s.rangeStart = start
	s.rangeLength = length
	return s.Storage.GetRange(ctx, id, start, length)
}
