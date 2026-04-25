package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stashysh/stashy/internal/storage"
	"github.com/stashysh/stashy/internal/storage/memory"
)

func TestServeFileFullResponse(t *testing.T) {
	svc, id := newTestService(t, "video/mp4", "0123456789")

	req := httptest.NewRequest(http.MethodGet, "/"+id, nil)
	rec := httptest.NewRecorder()

	svc.ServeFile(rec, req, id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "10" {
		t.Fatalf("Content-Length = %q, want 10", got)
	}
	if got := rec.Body.String(); got != "0123456789" {
		t.Fatalf("body = %q", got)
	}
}

func TestServeFileByteRange(t *testing.T) {
	base := memory.New()
	meta, err := base.Put(t.Context(), "user-1", "video/mp4", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	store := &rangeTrackingStore{Storage: base}
	svc := New(store, "http://example.test")

	req := httptest.NewRequest(http.MethodGet, "/"+meta.ID, nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()

	svc.ServeFile(rec, req, meta.ID)

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
	base := memory.New()
	meta, err := base.Put(t.Context(), "user-1", "video/mp4", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	store := &rangeTrackingStore{Storage: base}
	svc := New(store, "http://example.test")

	req := httptest.NewRequest(http.MethodHead, "/"+meta.ID, nil)
	rec := httptest.NewRecorder()

	svc.ServeFile(rec, req, meta.ID)

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
	base := memory.New()
	meta, err := base.Put(t.Context(), "user-1", "video/mp4", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	store := &rangeTrackingStore{Storage: base}
	svc := New(store, "http://example.test")

	req := httptest.NewRequest(http.MethodHead, "/"+meta.ID, nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()

	svc.ServeFile(rec, req, meta.ID)

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
	svc, id := newTestService(t, "video/mp4", "0123456789")

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
	svc, id := newTestService(t, "video/mp4", "0123456789")

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
	svc, id := newTestService(t, "video/mp4", "0123456789")

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

func newTestService(t *testing.T, contentType, body string) (*StorageService, string) {
	t.Helper()

	store := memory.New()
	meta, err := store.Put(t.Context(), "user-1", contentType, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	return New(store, "http://example.test"), meta.ID
}

type rangeTrackingStore struct {
	storage.Storage
	getCalls      int
	getRangeCalls int
	rangeStart    int64
	rangeLength   int64
}

func (s *rangeTrackingStore) Get(ctx context.Context, id string) (io.ReadCloser, *storage.FileMeta, error) {
	s.getCalls++
	return s.Storage.Get(ctx, id)
}

func (s *rangeTrackingStore) GetRange(ctx context.Context, id string, start, length int64) (io.ReadCloser, error) {
	s.getRangeCalls++
	s.rangeStart = start
	s.rangeLength = length
	return s.Storage.GetRange(ctx, id, start, length)
}
