package auth

import (
	"encoding/json"
	"net/http"

	"github.com/stashysh/stashy/internal/db"
)

type APIKeyHandler struct {
	db       *db.DB
	sessions *SessionManager
}

func NewAPIKeyHandler(database *db.DB, sessions *SessionManager) *APIKeyHandler {
	return &APIKeyHandler{db: database, sessions: sessions}
}

func (h *APIKeyHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/keys", h.handleCreate)
	mux.HandleFunc("GET /auth/keys", h.handleList)
	mux.HandleFunc("DELETE /auth/keys/{id}", h.handleDelete)
}

func (h *APIKeyHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.sessions.GetUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	label := r.FormValue("label")
	if label == "" {
		label = "default"
	}

	plaintext, key, err := h.db.CreateAPIKey(r.Context(), userID, label)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"key":        plaintext,
		"id":         key.ID,
		"label":      key.Label,
		"prefix":     key.KeyPrefix,
		"created_at": key.CreatedAt,
	})
}

func (h *APIKeyHandler) handleList(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.sessions.GetUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	keys, err := h.db.ListAPIKeys(r.Context(), userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type keyInfo struct {
		ID        string `json:"id"`
		Label     string `json:"label"`
		Prefix    string `json:"prefix"`
		CreatedAt string `json:"created_at"`
	}

	result := make([]keyInfo, len(keys))
	for i, k := range keys {
		result[i] = keyInfo{
			ID:        k.ID,
			Label:     k.Label,
			Prefix:    k.KeyPrefix,
			CreatedAt: k.CreatedAt.Format("2006-01-02 15:04"),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *APIKeyHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.sessions.GetUserID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	keyID := r.PathValue("id")
	if keyID == "" {
		http.Error(w, "invalid key id", http.StatusBadRequest)
		return
	}

	if err := h.db.DeleteAPIKey(r.Context(), keyID, userID); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
