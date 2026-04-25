package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/stashysh/stashy/internal/auth"
)

// ServeFile fetches id from storage and streams it directly to w.
// The caller is responsible for any authorization checks before calling this.
func (s *StorageService) ServeFile(w http.ResponseWriter, r *http.Request, id string) {
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" {
		s.serveFileRange(w, r, id, rangeHeader)
		return
	}

	if r.Method == http.MethodHead {
		s.serveFileHead(w, r, id)
		return
	}

	rc, meta, err := s.store.Get(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		log.Printf("ServeFile %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Accept-Ranges", "bytes")
	if meta.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	}
	if _, err := io.Copy(w, rc); err != nil {
		log.Printf("ServeFile %s: copy: %v", id, err)
	}
}

func (s *StorageService) serveFileHead(w http.ResponseWriter, r *http.Request, id string) {
	meta, err := s.store.Stat(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		log.Printf("ServeFile %s: stat: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Accept-Ranges", "bytes")
	if meta.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	}
}

func (s *StorageService) serveFileRange(w http.ResponseWriter, r *http.Request, id, rangeHeader string) {
	meta, err := s.store.Stat(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		log.Printf("ServeFile %s: stat: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Accept-Ranges", "bytes")
	byteRange, err := parseByteRange(rangeHeader, meta.Size)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", meta.Size))
		http.Error(w, "requested range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(byteRange.length(), 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", byteRange.start, byteRange.end, meta.Size))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusPartialContent)
		return
	}

	rc, err := s.store.GetRange(r.Context(), id, byteRange.start, byteRange.length())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
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

// HTTPDownload handles GET /v1/files/{id} directly, bypassing Vanguard.
// Auth is expected to be enforced by upstream middleware.
func (s *StorageService) HTTPDownload(w http.ResponseWriter, r *http.Request) {
	s.ServeFile(w, r, r.PathValue("id"))
}

// HTTPUpload handles POST /v1/files directly, bypassing Vanguard.
// Streaming r.Body straight to storage avoids the full-body buffering that
// Vanguard does when transcoding HttpBody RPCs (see github.com/stashysh/stashy/issues/23).
func (s *StorageService) HTTPUpload(w http.ResponseWriter, r *http.Request) {
	owner, _ := auth.UserIDFromContext(r.Context())

	ct, err := validateContentType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	meta, err := s.store.Put(r.Context(), owner, ct, r.Body)
	if err != nil {
		log.Printf("HTTPUpload: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}{ID: meta.ID, URL: s.hostname + "/" + meta.ID})
}

// HTTPReplace handles PUT /v1/files/{id} directly, bypassing Vanguard.
func (s *StorageService) HTTPReplace(w http.ResponseWriter, r *http.Request) {
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

	if _, err := s.store.Update(r.Context(), id, owner, ct, r.Body); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(err.Error(), "permission denied") {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		log.Printf("HTTPReplace %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte("{}"))
}
