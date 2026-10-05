package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
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

func sessionRequest(t *testing.T, sessions *auth.SessionManager, target, userID string) *http.Request {
	t.Helper()

	rec := httptest.NewRecorder()
	sessions.SetSession(rec, userID)

	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func TestFilesPageShowsLoginWithoutSession(t *testing.T) {
	database := newTestDB(t)
	sessions := auth.NewSessionManager("test-secret")
	h := NewHandler(database, sessions, "http://example.test", "dev")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	h.FilesPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Sign in with Google") {
		t.Fatalf("expected login page, got: %.200s", rec.Body.String())
	}
}

func TestFilesPageListsFiles(t *testing.T) {
	database := newTestDB(t)
	sessions := auth.NewSessionManager("test-secret")
	h := NewHandler(database, sessions, "http://example.test", "dev")

	user, err := database.UpsertUser(t.Context(), "g-1", "alice@example.com", "Alice")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	const fileID = "abc123abc123abc123abc" // 21 chars, nanoid-shaped
	f, err := database.CreateFile(t.Context(), db.File{ID: fileID, Owner: user.ID, Name: "logo.png", ContentType: "image/png", Size: 42})
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if err := database.SetFileSlug(t.Context(), f.ID, user.ID, "logo"); err != nil {
		t.Fatalf("SetFileSlug: %v", err)
	}

	rec := httptest.NewRecorder()
	h.FilesPage(rec, sessionRequest(t, sessions, "/", user.ID))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, want := range []string{fileID, "logo.png", "logo", "image/png", "42 B", "Internal", "/" + fileID + "/logo"} {
		if !strings.Contains(body, want) {
			t.Errorf("files page missing %q", want)
		}
	}
}

func TestKeysPageListsKeys(t *testing.T) {
	database := newTestDB(t)
	sessions := auth.NewSessionManager("test-secret")
	h := NewHandler(database, sessions, "http://example.test", "dev")

	user, err := database.UpsertUser(t.Context(), "g-1", "alice@example.com", "Alice")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if _, _, err := database.CreateAPIKey(t.Context(), user.ID, "production"); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	rec := httptest.NewRecorder()
	h.KeysPage(rec, sessionRequest(t, sessions, "/keys", user.ID))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, want := range []string{"production", "API Keys", "Revoke"} {
		if !strings.Contains(body, want) {
			t.Errorf("keys page missing %q", want)
		}
	}
}

func TestFormatSize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
		{3 * 1024 * 1024 * 1024, "3.0 GB"},
	}
	for _, tc := range cases {
		if got := formatSize(tc.n); got != tc.want {
			t.Errorf("formatSize(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestFooterShowsVersionToSignedInUsers(t *testing.T) {
	database := newTestDB(t)
	sessions := auth.NewSessionManager("test-secret")
	user, err := database.UpsertUser(t.Context(), "g-1", "alice@example.com", "Alice")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	for _, tc := range []struct {
		version, want string
	}{
		{"0.9.2", ">v0.9.2</p>"},
		{"dev", ">dev</p>"},
	} {
		h := NewHandler(database, sessions, "http://example.test", tc.version)

		rec := httptest.NewRecorder()
		h.KeysPage(rec, sessionRequest(t, sessions, "/keys", user.ID))
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("version %s: keys page missing %q", tc.version, tc.want)
		}

		rec = httptest.NewRecorder()
		h.FilesPage(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("version %s: login page shows the version", tc.version)
		}
	}
}

func TestFilesPageColumns(t *testing.T) {
	database := newTestDB(t)
	sessions := auth.NewSessionManager("test-secret")
	h := NewHandler(database, sessions, "http://example.test", "dev")
	user, err := database.UpsertUser(t.Context(), "g-1", "alice@example.com", "Alice")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	for id, name := range map[string]string{
		"named0000000000000000": "Report.pdf",
		"unnamed00000000000000": "",
	} {
		if _, err := database.CreateFile(t.Context(), db.File{ID: id, Owner: user.ID, Name: name, ContentType: "text/plain", Size: 1}); err != nil {
			t.Fatalf("CreateFile: %v", err)
		}
	}

	rec := httptest.NewRecorder()
	h.FilesPage(rec, sessionRequest(t, sessions, "/", user.ID))
	body := rec.Body.String()

	// IDs link to the file; names are plain text; every row has a copy icon.
	for _, want := range []string{
		">named0000000000000000</a>",
		">unnamed00000000000000</a>",
		">Report.pdf</td>",
		`aria-label="Copy URL"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("files page missing %q", want)
		}
	}
	if strings.Contains(body, ">Report.pdf</a>") {
		t.Errorf("names should not be links")
	}
}
