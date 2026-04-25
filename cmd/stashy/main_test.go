package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/service"
	"github.com/stashysh/stashy/internal/storage"
	"github.com/stashysh/stashy/internal/storage/memory"
)

func TestFileHandlerServesPublicByteRangesThroughStorageRange(t *testing.T) {
	base := memory.New()
	meta, err := base.Put(t.Context(), "user-1", "video/mp4", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := base.SetPublic(t.Context(), meta.ID, true); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}

	store := &rangeTrackingStore{Storage: base}
	files := service.New(store, "http://example.test")
	handler := fileHandler(store, files, auth.NewSessionManager("test-secret"))

	req := httptest.NewRequest(http.MethodGet, "/"+meta.ID, nil)
	req.SetPathValue("id", meta.ID)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusPartialContent)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q, want bytes 2-5/10", got)
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
}

func TestFileHandlerHeadDoesNotOpenBody(t *testing.T) {
	base := memory.New()
	meta, err := base.Put(t.Context(), "user-1", "video/mp4", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := base.SetPublic(t.Context(), meta.ID, true); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}

	store := &rangeTrackingStore{Storage: base}
	files := service.New(store, "http://example.test")
	handler := fileHandler(store, files, auth.NewSessionManager("test-secret"))

	req := httptest.NewRequest(http.MethodHead, "/"+meta.ID, nil)
	req.SetPathValue("id", meta.ID)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Length"); got != "10" {
		t.Fatalf("Content-Length = %q, want 10", got)
	}
	if store.getCalls != 0 {
		t.Fatalf("Get calls = %d, want 0", store.getCalls)
	}
	if store.getRangeCalls != 0 {
		t.Fatalf("GetRange calls = %d, want 0", store.getRangeCalls)
	}
}

type rangeTrackingStore struct {
	storage.Storage
	getCalls      int
	getRangeCalls int
}

func (s *rangeTrackingStore) Get(ctx context.Context, id string) (io.ReadCloser, *storage.FileMeta, error) {
	s.getCalls++
	return s.Storage.Get(ctx, id)
}

func (s *rangeTrackingStore) GetRange(ctx context.Context, id string, start, length int64) (io.ReadCloser, error) {
	s.getRangeCalls++
	return s.Storage.GetRange(ctx, id, start, length)
}
