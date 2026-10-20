package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stashysh/stashy/internal/storage/memory"
)

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
		rec := serveAs(http.HandlerFunc(svc.httpGetFileContent), tc.owner, req)
		if rec.Code != tc.want {
			t.Errorf("owner %s, id %s: status = %d, want %d", tc.owner, tc.id, rec.Code, tc.want)
		}
		if tc.want == http.StatusOK && rec.Body.String() != "hello" {
			t.Errorf("body = %q, want hello", rec.Body)
		}
	}
}

func TestChecksumETagAndConditionalRequests(t *testing.T) {
	svc, _ := newListService(t, 0)
	transcoder := newTestHandler(t, svc)

	// Upload: the response carries the checksum and an ETag.
	req := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader("hello"))
	req.Header.Set("Content-Type", "text/plain")
	rec := serveAs(transcoder, "1", req)
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create: %v; body: %s", err, rec.Body)
	}
	id := created["id"].(string)
	if created["checksum"] != "mnG7TA==" || rec.Header().Get("ETag") != `"mnG7TA=="` {
		t.Fatalf("create: checksum %v, ETag %q", created["checksum"], rec.Header().Get("ETag"))
	}

	get := func(method, ifNoneMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/v1/files/"+id+"/content", nil)
		req.SetPathValue("id", id)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		return serveAs(http.HandlerFunc(svc.httpGetFileContent), "1", req)
	}
	put := func(ifMatch, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/v1/files/"+id+"/content", strings.NewReader(body))
		req.SetPathValue("id", id)
		req.Header.Set("Content-Type", "text/plain")
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		return serveAs(http.HandlerFunc(svc.httpUpdateFileContent), "1", req)
	}

	// Download: ETag, and 304 when the client already has this version.
	if rec := get(http.MethodGet, ""); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"mnG7TA=="` || rec.Body.String() != "hello" {
		t.Fatalf("GET: %d, ETag %q, body %q", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	for _, inm := range []string{`"mnG7TA=="`, `W/"mnG7TA=="`, `"other", "mnG7TA=="`, "*"} {
		if rec := get(http.MethodGet, inm); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("GET If-None-Match %s: %d with %d body bytes, want 304 and none", inm, rec.Code, rec.Body.Len())
		}
	}
	if rec := get(http.MethodHead, `"mnG7TA=="`); rec.Code != http.StatusNotModified {
		t.Errorf("HEAD If-None-Match: %d, want 304", rec.Code)
	}
	if rec := get(http.MethodGet, `"stale=="`); rec.Code != http.StatusOK {
		t.Errorf("GET with a stale If-None-Match: %d, want 200", rec.Code)
	}

	// Conditional update: a stale or weak tag is rejected without writing.
	for _, stale := range []string{`"stale=="`, `W/"mnG7TA=="`} {
		if rec := put(stale, "lost update"); rec.Code != http.StatusPreconditionFailed {
			t.Errorf("PUT If-Match %s: %d, want 412", stale, rec.Code)
		}
	}
	if rec := get(http.MethodGet, ""); rec.Body.String() != "hello" {
		t.Fatalf("content changed by a rejected PUT: %q", rec.Body)
	}

	// The current tag succeeds and returns the new ETag.
	rec = put(`"mnG7TA=="`, "hello, world")
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT If-Match current: %d; body: %s", rec.Code, rec.Body)
	}
	newTag := rec.Header().Get("ETag")
	if newTag == "" || newTag == `"mnG7TA=="` {
		t.Fatalf("PUT: ETag %q, want a new tag", newTag)
	}
	if rec := get(http.MethodGet, ""); rec.Header().Get("ETag") != newTag || rec.Body.String() != "hello, world" {
		t.Fatalf("after PUT: ETag %q, body %q", rec.Header().Get("ETag"), rec.Body)
	}

	// "*" and no If-Match at all are unconditional.
	if rec := put("*", "star"); rec.Code != http.StatusOK {
		t.Errorf("PUT If-Match *: %d, want 200", rec.Code)
	}
	if rec := put("", "plain"); rec.Code != http.StatusOK {
		t.Errorf("PUT without If-Match: %d, want 200", rec.Code)
	}
}

// TestDirectHandlerErrorsMatchVanguard checks that the direct content handlers
// fail with the same JSON body and codes as the routes Vanguard serves.
func TestDirectHandlerErrorsMatchVanguard(t *testing.T) {
	svc, id := newTestService(t, memory.New(), "text/plain", "hello")
	h := newTestHandler(t, svc)
	const missing = "AAAAAAAAAAAAAAAAAAAAA"

	send := func(owner, method, target string, header ...string) *httptest.ResponseRecorder {
		var body io.Reader
		if method == http.MethodPut {
			body = strings.NewReader("new")
		}
		req := httptest.NewRequest(method, target, body)
		for i := 0; i < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		return serveAs(h, owner, req)
	}

	// A missing file through Vanguard and through a direct handler.
	vanguardCode, _ := apiErrorBody(t, send("1", http.MethodGet, "/v1/files/"+missing))
	directCode, message := apiErrorBody(t, send("1", http.MethodGet, "/v1/files/"+missing+"/content"))
	if vanguardCode != 5 || directCode != vanguardCode || !strings.Contains(message, "file not found") {
		t.Errorf("missing file: Vanguard code %d, direct code %d (%q), want 5 for both", vanguardCode, directCode, message)
	}

	for _, tc := range []struct {
		name         string
		rec          *httptest.ResponseRecorder
		status, code int
	}{
		{"other user's content", send("2", http.MethodGet, "/v1/files/"+id+"/content"), http.StatusForbidden, 7},
		{"stale If-Match", send("1", http.MethodPut, "/v1/files/"+id+"/content", "If-Match", `"stale=="`), http.StatusPreconditionFailed, 9},
		{"unsatisfiable range", send("1", http.MethodGet, "/v1/files/"+id+"/content", "Range", "bytes=10-20"), http.StatusRequestedRangeNotSatisfiable, 11},
		{"multipart replace", send("1", http.MethodPut, "/v1/files/"+id+"/content", "Content-Type", "multipart/form-data; boundary=x"), http.StatusBadRequest, 3},
	} {
		if tc.rec.Code != tc.status {
			t.Errorf("%s: status = %d, want %d", tc.name, tc.rec.Code, tc.status)
			continue
		}
		if code, _ := apiErrorBody(t, tc.rec); code != tc.code {
			t.Errorf("%s: code = %d, want %d", tc.name, code, tc.code)
		}
	}
}
