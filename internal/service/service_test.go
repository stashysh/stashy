package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"

	stashyv1 "github.com/stashysh/stashy/gen/stashy/v1"
	"github.com/stashysh/stashy/gen/stashy/v1/stashyv1connect"
	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
	"github.com/stashysh/stashy/internal/storage/memory"
)

// newListService builds a FileService with n files owned by "1" and one file
// owned by "2", and returns the service and the "2" file's id.
func newListService(t *testing.T, n int) (*FileService, string) {
	t.Helper()

	database := newTestDB(t)
	store := memory.New()
	for range n {
		putTestFile(t, database, store, "1", "text/plain", "x")
	}
	other := putTestFile(t, database, store, "2", "text/plain", "x")
	return New(store, database, "http://example.test"), other.ID
}

func listFiles(t *testing.T, svc *FileService, msg *stashyv1.ListFilesRequest) ([]*stashyv1.File, error) {
	t.Helper()

	ctx := auth.ContextWithUserID(t.Context(), "1")
	resp, err := svc.ListFiles(ctx, msg)
	if err != nil {
		return nil, err
	}
	return resp.Files, nil
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
func TestListFilesREST(t *testing.T) {
	svc, _ := newListService(t, 3)
	transcoder := newTestHandler(t, svc)

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
	transcoder := newTestHandler(t, svc)

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
	created := decode(t, serveAs(transcoder, "1", req))
	id, _ := created["id"].(string)
	if created["url"] != "http://example.test/"+id || created["size"] != "5" || created["content_type"] != "text/plain" {
		t.Fatalf("created = %v", created)
	}

	req = httptest.NewRequest(http.MethodPut, "/v1/files/"+id+"/content", strings.NewReader("hello, world"))
	req.SetPathValue("id", id)
	req.Header.Set("Content-Type", "text/markdown")
	replaced := decode(t, serveAs(http.HandlerFunc(svc.httpUpdateFileContent), "1", req))
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
		if _, ok := replaced[key]; !ok {
			t.Errorf("direct handler body is missing %q, which the transcoder emits", key)
		}
	}
	if len(replaced) != len(updated) {
		t.Errorf("direct handler emits %d fields, transcoder %d", len(replaced), len(updated))
	}
}

func TestUpdateFileWithoutFieldsChecksOwner(t *testing.T) {
	svc, otherID := newListService(t, 0)

	ctx := auth.ContextWithUserID(t.Context(), "1")
	_, err := svc.UpdateFile(ctx, &stashyv1.UpdateFileRequest{Id: otherID})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("code = %v, want %v", connect.CodeOf(err), connect.CodePermissionDenied)
	}
}

func TestGetFileREST(t *testing.T) {
	svc, otherID := newListService(t, 0)
	own := putTestFile(t, svc.db, svc.store, "1", "text/plain", "x")
	transcoder := newTestHandler(t, svc)

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

// TestConnectGetForReads checks that side-effect-free RPCs accept Connect GET
// requests through the transcoder.
func TestConnectGetForReads(t *testing.T) {
	svc, _ := newListService(t, 1)
	transcoder := newTestHandler(t, svc)

	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		transcoder.ServeHTTP(w, r.WithContext(auth.ContextWithUserID(r.Context(), "1")))
	}))
	t.Cleanup(srv.Close)

	client := stashyv1connect.NewFileServiceClient(connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL, connecthttp.WithHTTPGet(), connecthttp.WithProtoJSON())))
	resp, err := client.ListFiles(t.Context(), &stashyv1.ListFilesRequest{})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(resp.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(resp.Files))
	}
	if _, err := client.GetFile(t.Context(), &stashyv1.GetFileRequest{Id: resp.Files[0].Id}); err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if len(methods) != 2 || methods[0] != http.MethodGet || methods[1] != http.MethodGet {
		t.Fatalf("request methods = %v, want two GETs", methods)
	}
}

func TestFileNames(t *testing.T) {
	svc, _ := newListService(t, 0)
	transcoder := newTestHandler(t, svc)

	// Upload with a name and slug, as the desktop client does.
	req := httptest.NewRequest(http.MethodPost, "/v1/files?slug=q3-report.pdf&name="+url.QueryEscape("Quarterly report.pdf"), strings.NewReader("%PDF"))
	req.Header.Set("Content-Type", "application/pdf")
	rec := serveAs(transcoder, "1", req)
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
		return serveAs(http.HandlerFunc(svc.httpGetFileContent), "1", req).Header().Get("Content-Disposition")
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
		if rec := serveAs(transcoder, "1", req); rec.Code != http.StatusBadRequest {
			t.Errorf("create with %s: status %d, want 400", bad, rec.Code)
		}
	}
}

func TestFileVisibility(t *testing.T) {
	svc, _ := newListService(t, 0)
	transcoder := newTestHandler(t, svc)

	create := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/files"+query, strings.NewReader("x"))
		req.Header.Set("Content-Type", "text/plain")
		return serveAs(transcoder, "1", req)
	}
	visibilityOf := func(rec *httptest.ResponseRecorder) (string, string) {
		t.Helper()
		var f map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
			t.Fatalf("decoding %s: %v", rec.Body, err)
		}
		return f["id"].(string), f["visibility"].(string)
	}

	// Default is internal; upload can choose another level.
	if _, v := visibilityOf(create("")); v != "internal" {
		t.Fatalf("default visibility = %q, want internal", v)
	}
	id, v := visibilityOf(create("?visibility=private"))
	if v != "private" {
		t.Fatalf("visibility = %q, want private", v)
	}
	if rec := create("?visibility=everyone"); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown visibility: status %d, want 400", rec.Code)
	}

	// Change it over REST.
	req := httptest.NewRequest(http.MethodPatch, "/v1/files/"+id, strings.NewReader(`{"visibility": "public"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := serveAs(transcoder, "1", req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH: status %d; body: %s", rec.Code, rec.Body)
	}
	if _, v := visibilityOf(rec); v != "public" {
		t.Fatalf("visibility = %q after PATCH, want public", v)
	}

	req = httptest.NewRequest(http.MethodPatch, "/v1/files/"+id, strings.NewReader(`{"visibility": ""}`))
	req.Header.Set("Content-Type", "application/json")
	if rec := serveAs(transcoder, "1", req); rec.Code != http.StatusBadRequest {
		t.Fatalf("PATCH with empty visibility: status %d, want 400", rec.Code)
	}
}

// TestUploadFile checks that UploadFile stores a file for the given owner and
// applies the same metadata rules as CreateFile.
func TestUploadFile(t *testing.T) {
	svc, _ := newListService(t, 0)

	upload := func(meta db.File) (*db.File, error) {
		meta.Owner = "1"
		return svc.UploadFile(t.Context(), meta, strings.NewReader("hello"))
	}

	f, err := upload(db.File{Slug: "notes.txt", Name: "notes.txt", Visibility: db.VisibilityPrivate, ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if f.Owner != "1" || f.Size != 5 || f.Checksum != "mnG7TA==" || f.Visibility != db.VisibilityPrivate || f.Path() != "/"+f.ID+"/notes.txt" {
		t.Fatalf("UploadFile = %+v", f)
	}
	if f, err := upload(db.File{}); err != nil || f.ContentType != "application/octet-stream" {
		t.Fatalf("UploadFile without metadata = %+v, %v; want octet-stream", f, err)
	}

	for name, meta := range map[string]db.File{
		"slug":         {Slug: "has spaces"},
		"name":         {Name: "a/b.txt"},
		"visibility":   {Visibility: "everyone"},
		"content type": {ContentType: "multipart/form-data; boundary=x"},
	} {
		if _, err := upload(meta); !errors.Is(err, ErrInvalidUpload) {
			t.Errorf("bad %s: err = %v, want ErrInvalidUpload", name, err)
		}
	}
}
