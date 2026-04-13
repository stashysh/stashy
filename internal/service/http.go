package service

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/stashysh/stashy/internal/auth"
)

// ServeFile fetches id from storage and streams it directly to w.
// The caller is responsible for any authorization checks before calling this.
func (s *StorageService) ServeFile(w http.ResponseWriter, r *http.Request, id string) {
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
	if _, err := io.Copy(w, rc); err != nil {
		log.Printf("ServeFile %s: copy: %v", id, err)
	}
}

// HTTPDownload handles GET /api/v1/files/{id} directly, bypassing Vanguard.
// Auth is expected to be enforced by upstream middleware.
func (s *StorageService) HTTPDownload(w http.ResponseWriter, r *http.Request) {
	s.ServeFile(w, r, r.PathValue("id"))
}

// HTTPUpload handles POST /api/v1/files directly, bypassing Vanguard.
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

// HTTPReplace handles PUT /api/v1/files/{id} directly, bypassing Vanguard.
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
