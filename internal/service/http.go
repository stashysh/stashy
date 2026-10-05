package service

import (
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"buf.build/go/protovalidate"
	"google.golang.org/genproto/googleapis/api/httpbody"
	"google.golang.org/protobuf/encoding/protojson"

	stashyv1 "github.com/stashysh/stashy/gen/stashy/v1"
	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
)

// ServeFile fetches id and streams it directly to w. Metadata (content type,
// size) comes from the database; storage is only touched for the bytes.
// The caller is responsible for any authorization checks before calling this.
func (s *FileService) ServeFile(w http.ResponseWriter, r *http.Request, id string) {
	f, err := s.db.GetFile(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		log.Printf("ServeFile %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Set once for every path below: full GET, HEAD, and range responses.
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Accept-Ranges", "bytes")
	if f.Name != "" {
		// inline keeps browsers displaying the file; the filename is used
		// when it is saved. FormatMediaType encodes non-ASCII names per RFC 2231.
		if cd := mime.FormatMediaType("inline", map[string]string{"filename": f.Name}); cd != "" {
			w.Header().Set("Content-Disposition", cd)
		}
	}

	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		s.serveFileRange(w, r, id, f.Size, rangeHeader)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	if r.Method == http.MethodHead {
		return
	}

	rc, err := s.store.Get(r.Context(), id)
	if err != nil {
		log.Printf("ServeFile %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	if _, err := io.Copy(w, rc); err != nil {
		log.Printf("ServeFile %s: copy: %v", id, err)
	}
}

// serveFileRange handles Range requests. Content-Type and Accept-Ranges are
// already set by ServeFile.
func (s *FileService) serveFileRange(w http.ResponseWriter, r *http.Request, id string, size int64, rangeHeader string) {
	byteRange, err := parseByteRange(rangeHeader, size)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		http.Error(w, "requested range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(byteRange.length(), 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", byteRange.start, byteRange.end, size))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusPartialContent)
		return
	}

	rc, err := s.store.GetRange(r.Context(), id, byteRange.start, byteRange.length())
	if err != nil {
		log.Printf("ServeFile %s: get range: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	w.WriteHeader(http.StatusPartialContent)
	if _, err := io.CopyN(w, rc, byteRange.length()); err != nil {
		log.Printf("ServeFile %s: copy: %v", id, err)
	}
}

type byteRange struct {
	start int64
	end   int64
}

func (r byteRange) length() int64 {
	return r.end - r.start + 1
}

func parseByteRange(header string, size int64) (byteRange, error) {
	if size <= 0 || !strings.HasPrefix(header, "bytes=") {
		return byteRange{}, fmt.Errorf("invalid range")
	}

	spec := strings.TrimSpace(strings.TrimPrefix(header, "bytes="))
	if spec == "" || strings.Contains(spec, ",") {
		return byteRange{}, fmt.Errorf("invalid range")
	}

	startText, endText, ok := strings.Cut(spec, "-")
	if !ok {
		return byteRange{}, fmt.Errorf("invalid range")
	}

	if startText == "" {
		suffixLength, err := strconv.ParseInt(endText, 10, 64)
		if err != nil || suffixLength <= 0 {
			return byteRange{}, fmt.Errorf("invalid range")
		}
		if suffixLength > size {
			suffixLength = size
		}
		return byteRange{start: size - suffixLength, end: size - 1}, nil
	}

	start, err := strconv.ParseInt(startText, 10, 64)
	if err != nil || start < 0 || start >= size {
		return byteRange{}, fmt.Errorf("invalid range")
	}

	end := size - 1
	if endText != "" {
		end, err = strconv.ParseInt(endText, 10, 64)
		if err != nil || end < start {
			return byteRange{}, fmt.Errorf("invalid range")
		}
		if end >= size {
			end = size - 1
		}
	}

	return byteRange{start: start, end: end}, nil
}

// HTTPGetFileContent handles GET /v1/files/{id}/content directly, bypassing
// Vanguard. Authentication is enforced by upstream middleware; ownership is
// checked here.
func (s *FileService) HTTPGetFileContent(w http.ResponseWriter, r *http.Request) {
	owner, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")
	if err := s.db.CheckFileOwner(r.Context(), id, owner); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(err.Error(), "permission denied") {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		log.Printf("HTTPGetFileContent %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.ServeFile(w, r, id)
}

// HTTPCreateFile handles POST /v1/files directly, bypassing Vanguard.
// Streaming r.Body straight to storage avoids the full-body buffering that
// Vanguard does when transcoding HttpBody RPCs (see github.com/stashysh/stashy/issues/23).
func (s *FileService) HTTPCreateFile(w http.ResponseWriter, r *http.Request) {
	owner, _ := auth.UserIDFromContext(r.Context())

	ct, err := validateContentType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	query := r.URL.Query()
	slug, name := query.Get("slug"), query.Get("name")
	// Validate against the same rules as the CreateFile RPC.
	msg := &stashyv1.CreateFileRequest{Content: &httpbody.HttpBody{}, Slug: &slug, Name: &name}
	if err := protovalidate.Validate(msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	f, err := s.putFile(r.Context(), owner, slug, name, ct, r.Body)
	if err != nil {
		log.Printf("HTTPCreateFile: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.writeFileJSON(w, f)
}

// HTTPUpdateFileContent handles PUT /v1/files/{id}/content directly, bypassing
// Vanguard.
func (s *FileService) HTTPUpdateFileContent(w http.ResponseWriter, r *http.Request) {
	owner, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	ct, err := validateContentType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	f, err := s.replaceFile(r.Context(), id, owner, ct, r.Body)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(err.Error(), "permission denied") {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		log.Printf("HTTPUpdateFileContent %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.writeFileJSON(w, f)
}

// fileJSON matches the Vanguard JSON codec options in cmd/stashy, so the
// direct handlers return the same body as the transcoded RPCs.
var fileJSON = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}

// writeFileJSON writes f as the REST body of a file-returning RPC, which is
// the bare File (response_body: "file").
func (s *FileService) writeFileJSON(w http.ResponseWriter, f *db.File) {
	b, err := fileJSON.Marshal(s.fileProto(f))
	if err != nil {
		log.Printf("marshaling file %s: %v", f.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}
