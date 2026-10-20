package service

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/genproto/googleapis/api/httpbody"

	stashyv1 "github.com/stashysh/stashy/gen/stashy/v1"
	"github.com/stashysh/stashy/gen/stashy/v1/stashyv1connect"
	"github.com/stashysh/stashy/internal/auth"
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

// TestConnectUploadLimit checks the Connect per-message limit: an upload
// larger than 4 MiB works when streamed in chunks, while a single message of
// that size is rejected rather than buffered.
func TestConnectUploadLimit(t *testing.T) {
	svc, _ := newListService(t, 0)
	h := newTestHandler(t, svc)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auth.ContextWithUserID(r.Context(), "1")))
	}))
	t.Cleanup(srv.Close)
	client := stashyv1connect.NewFileServiceClient(connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL)))

	content := bytes.Repeat([]byte("x"), 5<<20)
	upload := func(chunk int) (*stashyv1.CreateFileResponse, error) {
		stream, err := client.CreateFile(t.Context())
		if err != nil {
			return nil, err
		}
		for off := 0; off < len(content); off += chunk {
			msg := &stashyv1.CreateFileRequest{Content: &httpbody.HttpBody{
				ContentType: "application/octet-stream",
				Data:        content[off:min(off+chunk, len(content))],
			}}
			if err := stream.Send(msg); err != nil {
				break // the server rejected the stream; CloseAndReceive reports why
			}
		}
		return stream.CloseAndReceive()
	}

	resp, err := upload(1 << 20)
	if err != nil {
		t.Fatalf("chunked upload: %v", err)
	}
	if resp.File.Size != int64(len(content)) {
		t.Fatalf("chunked upload: size = %d, want %d", resp.File.Size, len(content))
	}
	if _, err := upload(len(content)); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("single-message upload: err = %v, want resource_exhausted", err)
	}
}
