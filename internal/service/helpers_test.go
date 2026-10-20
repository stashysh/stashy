package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
	"github.com/stashysh/stashy/internal/storage"
)

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

// putTestFile stores body under a new id and records its metadata row.
func putTestFile(t *testing.T, database *db.DB, store storage.Storage, owner, contentType, body string) *db.File {
	t.Helper()

	id, err := storage.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, err := store.Put(t.Context(), id, contentType, strings.NewReader(body)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	f, err := database.CreateFile(t.Context(), db.File{ID: id, Owner: owner, ContentType: contentType, Size: int64(len(body))})
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	return f
}

// newTestService builds a FileService over store with one stored file owned
// by "1" and returns the service and the file's id.
func newTestService(t *testing.T, store storage.Storage, contentType, body string) (*FileService, string) {
	t.Helper()

	database := newTestDB(t)
	f := putTestFile(t, database, store, "1", contentType, body)
	return New(store, database, "http://example.test"), f.ID
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

// newTestHandler serves svc's API routes as cmd/stashy does, minus
// authentication; serveAs supplies the user.
func newTestHandler(t *testing.T, svc *FileService) http.Handler {
	t.Helper()

	mux := http.NewServeMux()
	if err := svc.mount(mux, func(h http.Handler) http.Handler { return h }, "test"); err != nil {
		t.Fatalf("mount: %v", err)
	}
	return mux
}

// serveAs runs req through h as the given user.
func serveAs(h http.Handler, owner string, req *http.Request) *httptest.ResponseRecorder {
	req = req.WithContext(auth.ContextWithUserID(req.Context(), owner))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
