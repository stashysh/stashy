package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stashysh/stashy/internal/storage/memory"
)

// TestMountRequiresAPIKey checks that every protocol Mount serves rejects
// requests without a valid key, and that a valid key gets through.
func TestMountRequiresAPIKey(t *testing.T) {
	database := newTestDB(t)
	svc := New(memory.New(), database, "http://example.test")
	mux := http.NewServeMux()
	if err := svc.Mount(mux, "test"); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	user, err := database.UpsertUser(t.Context(), "alice", "alice@example.com", "alice")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	key, _, err := database.CreateAPIKey(t.Context(), user.ID, "test")
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/files", ""},
		{http.MethodGet, "/v1/files/abc/content", ""},
		{http.MethodPost, "/stashy.v1.FileService/ListFiles", "{}"},
		{http.MethodPost, "/mcp", "{}"},
	} {
		for _, header := range []string{"", "Bearer not-a-key"} {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with Authorization %q: status = %d, want 401", tc.method, tc.path, header, rec.Code)
				continue
			}
			// MCP answers in the SDK's own form; the rest share the API's.
			if tc.path != "/mcp" {
				if code, _ := apiErrorBody(t, rec); code != 16 {
					t.Errorf("%s %s with Authorization %q: code = %d, want 16", tc.method, tc.path, header, code)
				}
			}
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/files with a valid key: status = %d; body: %s", rec.Code, rec.Body)
	}
}
