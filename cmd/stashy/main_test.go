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

func TestFileHandlerCanonicalizesSlug(t *testing.T) {
	store := memory.New()
	meta, err := store.Put(t.Context(), "user-1", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.SetPublic(t.Context(), meta.ID, true); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	if err := store.SetSlug(t.Context(), meta.ID, "user-1", "my-photo"); err != nil {
		t.Fatalf("SetSlug: %v", err)
	}

	files := service.New(store, "http://example.test")
	handler := fileHandler(store, files, auth.NewSessionManager("test-secret"))

	canonical := "/" + meta.ID + "/my-photo"

	cases := []struct {
		name     string
		urlSlug  string // "" means request /{id} with no slug segment
		wantCode int
		wantLoc  string
		wantBody string
	}{
		{"bare id redirects to slug", "", http.StatusFound, canonical, ""},
		{"correct slug serves", "my-photo", http.StatusOK, "", "hello"},
		{"stale slug redirects to canonical", "old-name", http.StatusFound, canonical, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := "/" + meta.ID
			if tc.urlSlug != "" {
				path += "/" + tc.urlSlug
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			handler(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if got := rec.Header().Get("Location"); got != tc.wantLoc {
				t.Fatalf("Location = %q, want %q", got, tc.wantLoc)
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
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
