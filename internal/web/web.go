package web

import (
	"embed"
	"html/template"
	"log"
	"net/http"

	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
)

//go:embed templates/*.html
var templateFS embed.FS

var templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

type Handler struct {
	db       *db.DB
	sessions *auth.SessionManager
}

func NewHandler(database *db.DB, sessions *auth.SessionManager) *Handler {
	return &Handler{db: database, sessions: sessions}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.sessions.GetUserID(r)
	if !ok {
		if err := templates.ExecuteTemplate(w, "login.html", nil); err != nil {
			log.Printf("rendering login: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	user, err := h.db.GetUserByID(r.Context(), userID)
	if err != nil {
		h.sessions.ClearSession(w)
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	keys, err := h.db.ListAPIKeys(r.Context(), userID)
	if err != nil {
		log.Printf("listing keys: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := struct {
		User *db.User
		Keys []db.APIKey
	}{
		User: user,
		Keys: keys,
	}

	if err := templates.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		log.Printf("rendering dashboard: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
