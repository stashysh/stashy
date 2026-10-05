package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"connectrpc.com/vanguard"
	"google.golang.org/protobuf/proto"

	stashyv1 "github.com/stashysh/stashy/gen/stashy/v1"
	"github.com/stashysh/stashy/gen/stashy/v1/stashyv1connect"
	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
	"github.com/stashysh/stashy/internal/storage"
	"github.com/stashysh/stashy/internal/storage/memory"
)

// newListService builds a FileService with n files owned by "1" and one file
// owned by "2", and returns the service and the "2" file's id.
func newListService(t *testing.T, n int) (*FileService, string) {
	t.Helper()

	database := newTestDB(t)
	for range n {
		createTestFile(t, database, "1")
	}
	other := createTestFile(t, database, "2")
	return New(memory.New(), database, "http://example.test"), other.ID
}

func createTestFile(t *testing.T, database *db.DB, owner string) *db.File {
	t.Helper()

	id, err := storage.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	f, err := database.CreateFile(t.Context(), id, owner, "", "", "text/plain", 1)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	return f
}

func listFiles(t *testing.T, svc *FileService, msg *stashyv1.ListFilesRequest) ([]*stashyv1.File, error) {
	t.Helper()

	ctx := auth.ContextWithUserID(t.Context(), "1")
	resp, err := svc.ListFiles(ctx, connect.NewRequest(msg))
	if err != nil {
		return nil, err
	}
	return resp.Msg.Files, nil
}

func TestListFilesPagesNewestFirst(t *testing.T) {
	svc, _ := newListService(t, 5)

	all, err := listFiles(t, svc, &stashyv1.ListFilesRequest{})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("got %d files, want 5 (other owner's file must be excluded)", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].CreatedAt.AsTime().After(all[i-1].CreatedAt.AsTime()) {
			t.Fatalf("files[%d] is newer than files[%d]", i, i-1)
		}
	}

	var paged []*stashyv1.File
	req := &stashyv1.ListFilesRequest{Limit: proto.Int32(2)}
	for {
		page, err := listFiles(t, svc, req)
		if err != nil {
			t.Fatalf("ListFiles: %v", err)
		}
		paged = append(paged, page...)
		if len(page) < 2 {
			break
		}
		req.After = proto.String(page[len(page)-1].Id)
	}

	if len(paged) != len(all) {
		t.Fatalf("paged %d files, want %d", len(paged), len(all))
	}
	for i := range all {
		if paged[i].Id != all[i].Id {
			t.Fatalf("paged[%d] = %s, want %s", i, paged[i].Id, all[i].Id)
		}
	}
}

func TestListFilesRejectsUnknownOrForeignAfter(t *testing.T) {
	svc, otherID := newListService(t, 1)

	for name, after := range map[string]string{
		"unknown": "AAAAAAAAAAAAAAAAAAAAA",
		"foreign": otherID,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := listFiles(t, svc, &stashyv1.ListFilesRequest{After: proto.String(after)})
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("code = %v, want %v", connect.CodeOf(err), connect.CodeInvalidArgument)
			}
		})
	}
}

// TestListFilesREST checks the REST mapping: a bare JSON array body and
// protovalidate enforcement of limit.
// newTestTranscoder serves svc's REST mapping with the codec options used in
// cmd/stashy.
func newTestTranscoder(t *testing.T, svc *FileService) http.Handler {
	t.Helper()

	path, handler := stashyv1connect.NewFileServiceHandler(svc, connect.WithInterceptors(validate.NewInterceptor()))
	transcoder, err := vanguard.NewTranscoder([]*vanguard.Service{vanguard.NewService(path, handler)},
		vanguard.WithCodec(func(res vanguard.TypeResolver) vanguard.Codec {
			codec := vanguard.NewJSONCodec(res)
			codec.MarshalOptions.UseProtoNames = true
			codec.MarshalOptions.EmitUnpopulated = true
			return codec
		}),
	)
	if err != nil {
		t.Fatalf("NewTranscoder: %v", err)
	}
	return transcoder
}

// serveAs runs req through h as the given user.
func serveAs(h http.Handler, owner string, req *http.Request) *httptest.ResponseRecorder {
	req = req.WithContext(auth.ContextWithUserID(req.Context(), owner))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListFilesREST(t *testing.T) {
	svc, _ := newListService(t, 3)
	transcoder := newTestTranscoder(t, svc)

	get := func(query string) *httptest.ResponseRecorder {
		return serveAs(transcoder, "1", httptest.NewRequest(http.MethodGet, "/v1/files"+query, nil))
	}

	rec := get("?limit=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var files []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &files); err != nil {
		t.Fatalf("body is not a JSON array: %v; body: %s", err, rec.Body)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
	if _, ok := files[0]["created_at"]; !ok {
		t.Fatalf("file is missing created_at: %v", files[0])
	}

	for _, q := range []string{"?limit=0", "?limit=101", "?after=short"} {
		if rec := get(q); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status = %d, want 400", q, rec.Code)
		}
	}
}

// TestWriteRPCsReturnFile checks that upload, replace, and update return the
// bare File over REST, with the same JSON shape on the direct handlers and
// the transcoded path.
func TestWriteRPCsReturnFile(t *testing.T) {
	svc, _ := newListService(t, 0)
	transcoder := newTestTranscoder(t, svc)

	decode := func(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
		}
		var f map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
			t.Fatalf("decoding body: %v; body: %s", err, rec.Body)
		}
		return f
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader("hello"))
	req.Header.Set("Content-Type", "text/plain")
	created := decode(t, serveAs(http.HandlerFunc(svc.HTTPCreateFile), "1", req))
	id, _ := created["id"].(string)
	if created["url"] != "http://example.test/"+id || created["size"] != "5" || created["content_type"] != "text/plain" {
		t.Fatalf("created = %v", created)
	}

	req = httptest.NewRequest(http.MethodPut, "/v1/files/"+id+"/content", strings.NewReader("hello, world"))
	req.SetPathValue("id", id)
	req.Header.Set("Content-Type", "text/markdown")
	replaced := decode(t, serveAs(http.HandlerFunc(svc.HTTPUpdateFileContent), "1", req))
	if replaced["size"] != "12" || replaced["content_type"] != "text/markdown" {
		t.Fatalf("replaced = %v", replaced)
	}

	req = httptest.NewRequest(http.MethodPatch, "/v1/files/"+id, strings.NewReader(`{"slug":"notes.md"}`))
	req.Header.Set("Content-Type", "application/json")
	updated := decode(t, serveAs(transcoder, "1", req))
	if updated["slug"] != "notes.md" || updated["url"] != "http://example.test/"+id+"/notes.md" {
		t.Fatalf("updated = %v", updated)
	}

	for key := range updated {
		if _, ok := created[key]; !ok {
			t.Errorf("direct handler body is missing %q, which the transcoder emits", key)
		}
	}
	if len(created) != len(updated) {
		t.Errorf("direct handler emits %d fields, transcoder %d", len(created), len(updated))
	}
}

func TestUpdateFileWithoutFieldsChecksOwner(t *testing.T) {
	svc, otherID := newListService(t, 0)

	ctx := auth.ContextWithUserID(t.Context(), "1")
	_, err := svc.UpdateFile(ctx, connect.NewRequest(&stashyv1.UpdateFileRequest{Id: otherID}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("code = %v, want %v", connect.CodeOf(err), connect.CodePermissionDenied)
	}
}

func TestGetFileREST(t *testing.T) {
	svc, otherID := newListService(t, 0)
	own := createTestFile(t, svc.db, "1")
	transcoder := newTestTranscoder(t, svc)

	rec := serveAs(transcoder, "1", httptest.NewRequest(http.MethodGet, "/v1/files/"+own.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var f map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatalf("decoding body: %v; body: %s", err, rec.Body)
	}
	if f["id"] != own.ID || f["content_type"] != "text/plain" {
		t.Fatalf("file = %v", f)
	}

	if rec := serveAs(transcoder, "1", httptest.NewRequest(http.MethodGet, "/v1/files/"+otherID, nil)); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign file: status = %d, want 403", rec.Code)
	}
}

func TestHTTPGetFileContentChecksOwner(t *testing.T) {
	svc, id := newTestService(t, memory.New(), "text/plain", "hello")

	for _, tc := range []struct {
		owner, id string
		want      int
	}{
		{"1", id, http.StatusOK},
		{"2", id, http.StatusForbidden},
		{"1", "AAAAAAAAAAAAAAAAAAAAA", http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/files/"+tc.id+"/content", nil)
		req.SetPathValue("id", tc.id)
		rec := serveAs(http.HandlerFunc(svc.HTTPGetFileContent), tc.owner, req)
		if rec.Code != tc.want {
			t.Errorf("owner %s, id %s: status = %d, want %d", tc.owner, tc.id, rec.Code, tc.want)
		}
		if tc.want == http.StatusOK && rec.Body.String() != "hello" {
			t.Errorf("body = %q, want hello", rec.Body)
		}
	}
}

// TestConnectGetForReads checks that side-effect-free RPCs accept Connect GET
// requests through the transcoder.
func TestConnectGetForReads(t *testing.T) {
	svc, _ := newListService(t, 1)
	transcoder := newTestTranscoder(t, svc)

	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		transcoder.ServeHTTP(w, r.WithContext(auth.ContextWithUserID(r.Context(), "1")))
	}))
	t.Cleanup(srv.Close)

	client := stashyv1connect.NewFileServiceClient(srv.Client(), srv.URL, connect.WithHTTPGet(), connect.WithProtoJSON())
	resp, err := client.ListFiles(t.Context(), connect.NewRequest(&stashyv1.ListFilesRequest{}))
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(resp.Msg.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(resp.Msg.Files))
	}
	if _, err := client.GetFile(t.Context(), connect.NewRequest(&stashyv1.GetFileRequest{Id: resp.Msg.Files[0].Id})); err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if len(methods) != 2 || methods[0] != http.MethodGet || methods[1] != http.MethodGet {
		t.Fatalf("request methods = %v, want two GETs", methods)
	}
}

func TestFileNames(t *testing.T) {
	svc, _ := newListService(t, 0)
	transcoder := newTestTranscoder(t, svc)

	// Upload with a name and slug, as the desktop client does.
	req := httptest.NewRequest(http.MethodPost, "/v1/files?slug=q3-report.pdf&name="+url.QueryEscape("Quarterly report.pdf"), strings.NewReader("%PDF"))
	req.Header.Set("Content-Type", "application/pdf")
	rec := serveAs(http.HandlerFunc(svc.HTTPCreateFile), "1", req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d; body: %s", rec.Code, rec.Body)
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create: %v", err)
	}
	id, _ := created["id"].(string)
	if created["name"] != "Quarterly report.pdf" || created["slug"] != "q3-report.pdf" || created["url"] != "http://example.test/"+id+"/q3-report.pdf" {
		t.Fatalf("create: %v", created)
	}

	// Download suggests the name; non-ASCII names are RFC 2231 encoded.
	download := func() string {
		req := httptest.NewRequest(http.MethodGet, "/v1/files/"+id+"/content", nil)
		req.SetPathValue("id", id)
		return serveAs(http.HandlerFunc(svc.HTTPGetFileContent), "1", req).Header().Get("Content-Disposition")
	}
	if cd := download(); cd != `inline; filename="Quarterly report.pdf"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	// Rename over REST, then clear.
	patch := func(body string) map[string]any {
		req := httptest.NewRequest(http.MethodPatch, "/v1/files/"+id, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := serveAs(transcoder, "1", req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH %s: status %d; body: %s", body, rec.Code, rec.Body)
		}
		var f map[string]any
		json.Unmarshal(rec.Body.Bytes(), &f)
		return f
	}
	if f := patch(`{"name": "Отчёт.pdf"}`); f["name"] != "Отчёт.pdf" {
		t.Fatalf("rename: name = %v", f["name"])
	}
	if cd := download(); cd != `inline; filename*=utf-8''%D0%9E%D1%82%D1%87%D1%91%D1%82.pdf` {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if f := patch(`{"name": ""}`); f["name"] != "" {
		t.Fatalf("clear: name = %v", f["name"])
	}
	if cd := download(); cd != "" {
		t.Fatalf("Content-Disposition = %q without a name, want none", cd)
	}

	// Names can't contain path separators or control characters, and slugs
	// are URL-safe.
	for _, bad := range []string{
		"name=" + url.QueryEscape("a/b.txt"),
		"name=" + url.QueryEscape("line\nbreak"),
		"name=" + strings.Repeat("x", 256),
		"slug=" + url.QueryEscape("has spaces"),
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/files?"+bad, strings.NewReader("x"))
		req.Header.Set("Content-Type", "text/plain")
		if rec := serveAs(http.HandlerFunc(svc.HTTPCreateFile), "1", req); rec.Code != http.StatusBadRequest {
			t.Errorf("create with %s: status %d, want 400", bad, rec.Code)
		}
	}
}
