package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/stashysh/stashy/internal/db"
	"github.com/stashysh/stashy/internal/service"
)

// upload stores the request body as a new file for the signed-in user. Like
// POST /v1/files, it takes the content type from the Content-Type header and
// the slug, name, and visibility from the query.
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.sessions.GetUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	query := r.URL.Query()
	f, err := h.files.UploadFile(r.Context(), db.File{
		Owner:       userID,
		Slug:        query.Get("slug"),
		Name:        query.Get("name"),
		Visibility:  query.Get("visibility"),
		ContentType: r.Header.Get("Content-Type"),
	}, r.Body)
	if errors.Is(err, service.ErrInvalidUpload) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("upload: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"id":  f.ID,
		"url": h.hostname + f.Path(),
	})
}
