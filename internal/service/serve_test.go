package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
	"github.com/stashysh/stashy/internal/storage/memory"
)

func TestServeFileFullResponse(t *testing.T) {
	svc, id := newTestService(t, memory.New(), "video/mp4", "0123456789")

	req := httptest.NewRequest(http.MethodGet, "/"+id, nil)
	rec := httptest.NewRecorder()

	svc.ServeFile(rec, req, id, http.Error)

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

	svc.ServeFile(rec, req, id, http.Error)

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

	svc.ServeFile(rec, req, id, http.Error)

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

	svc.ServeFile(rec, req, id, http.Error)

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

	svc.ServeFile(rec, req, id, http.Error)

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

	svc.ServeFile(rec, req, id, http.Error)

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

	svc.ServeFile(rec, req, id, http.Error)

	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestedRangeNotSatisfiable)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes */10" {
		t.Fatalf("Content-Range = %q, want bytes */10", got)
	}
}

func TestPublicFileHandlerServesPublicByteRangesThroughStorageRange(t *testing.T) {
	database := newTestDB(t)
	base := memory.New()
	f := putTestFile(t, database, base, "1", "video/mp4", "0123456789")
	if err := database.SetFileVisibility(t.Context(), f.ID, "1", db.VisibilityPublic); err != nil {
		t.Fatalf("SetFileVisibility: %v", err)
	}

	store := &rangeTrackingStore{Storage: base}
	files := New(store, database, "http://example.test")
	handler := files.PublicFileHandler(auth.NewSessionManager("test-secret"))

	req := httptest.NewRequest(http.MethodGet, "/"+f.ID, nil)
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

func TestPublicFileHandlerHeadDoesNotOpenBody(t *testing.T) {
	database := newTestDB(t)
	base := memory.New()
	f := putTestFile(t, database, base, "1", "video/mp4", "0123456789")
	if err := database.SetFileVisibility(t.Context(), f.ID, "1", db.VisibilityPublic); err != nil {
		t.Fatalf("SetFileVisibility: %v", err)
	}

	store := &rangeTrackingStore{Storage: base}
	files := New(store, database, "http://example.test")
	handler := files.PublicFileHandler(auth.NewSessionManager("test-secret"))

	req := httptest.NewRequest(http.MethodHead, "/"+f.ID, nil)
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

func TestPublicFileHandlerCanonicalizesSlug(t *testing.T) {
	database := newTestDB(t)
	store := memory.New()
	f := putTestFile(t, database, store, "1", "text/plain", "hello")
	if err := database.SetFileVisibility(t.Context(), f.ID, "1", db.VisibilityPublic); err != nil {
		t.Fatalf("SetFileVisibility: %v", err)
	}
	if err := database.SetFileSlug(t.Context(), f.ID, "1", "my-photo"); err != nil {
		t.Fatalf("SetFileSlug: %v", err)
	}

	files := New(store, database, "http://example.test")
	handler := files.PublicFileHandler(auth.NewSessionManager("test-secret"))

	canonical := "/" + f.ID + "/my-photo"

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
			path := "/" + f.ID
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

func TestPublicFileHandlerRedirectsPrivateBrowserRequestToLogin(t *testing.T) {
	database := newTestDB(t)
	store := memory.New()
	f := putTestFile(t, database, store, "1", "text/plain", "hello")
	files := New(store, database, "http://example.test")
	handler := files.PublicFileHandler(auth.NewSessionManager("test-secret"))

	req := httptest.NewRequest(http.MethodGet, "/"+f.ID+"?download=1", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTemporaryRedirect)
	}
	want := "/auth/google/login?next=%2F" + f.ID + "%3Fdownload%3D1"
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestPublicFileHandlerReturnsUnauthorizedForPrivateNonBrowserRequest(t *testing.T) {
	database := newTestDB(t)
	store := memory.New()
	f := putTestFile(t, database, store, "1", "text/plain", "hello")
	files := New(store, database, "http://example.test")
	handler := files.PublicFileHandler(auth.NewSessionManager("test-secret"))

	req := httptest.NewRequest(http.MethodGet, "/"+f.ID, nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestPublicFileHandlerEnforcesVisibility(t *testing.T) {
	database := newTestDB(t)
	store := memory.New()
	files := New(store, database, "http://example.test")
	sessions := auth.NewSessionManager("test-secret")
	handler := files.PublicFileHandler(sessions)

	// get requests f as viewer: "" is signed out, otherwise a signed-in user ID.
	get := func(f *db.File, viewer string) int {
		req := httptest.NewRequest(http.MethodGet, "/"+f.ID, nil)
		req.Header.Set("Accept", "application/json")
		if viewer != "" {
			cookies := httptest.NewRecorder()
			sessions.SetSession(cookies, viewer)
			for _, c := range cookies.Result().Cookies() {
				req.AddCookie(c)
			}
		}
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec.Code
	}

	for _, tc := range []struct {
		visibility              string
		signedOut, other, owner int
	}{
		{db.VisibilityPublic, http.StatusOK, http.StatusOK, http.StatusOK},
		{db.VisibilityInternal, http.StatusUnauthorized, http.StatusOK, http.StatusOK},
		{db.VisibilityPrivate, http.StatusUnauthorized, http.StatusNotFound, http.StatusOK},
	} {
		f := putTestFile(t, database, store, "1", "text/plain", "hello")
		if err := database.SetFileVisibility(t.Context(), f.ID, "1", tc.visibility); err != nil {
			t.Fatalf("SetFileVisibility: %v", err)
		}
		for viewer, want := range map[string]int{"": tc.signedOut, "2": tc.other, "1": tc.owner} {
			if got := get(f, viewer); got != want {
				t.Errorf("%s file, viewer %q: status = %d, want %d", tc.visibility, viewer, got, want)
			}
		}
	}
}

// TestPublicFileHandlerErrorsArePlainText checks that public file links keep
// plain-text errors for browsers, unlike the API.
func TestPublicFileHandlerErrorsArePlainText(t *testing.T) {
	database := newTestDB(t)
	store := memory.New()
	f := putTestFile(t, database, store, "1", "text/plain", "0123456789")
	if err := database.SetFileVisibility(t.Context(), f.ID, "1", db.VisibilityPublic); err != nil {
		t.Fatalf("SetFileVisibility: %v", err)
	}
	handler := New(store, database, "http://example.test").PublicFileHandler(auth.NewSessionManager("test-secret"))

	req := httptest.NewRequest(http.MethodGet, "/"+f.ID, nil)
	req.Header.Set("Range", "bytes=10-20")
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("status %d, Content-Type %q; want 416 as plain text", rec.Code, rec.Header().Get("Content-Type"))
	}
}
