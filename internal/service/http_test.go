package service

import (
	"encoding/json"
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
