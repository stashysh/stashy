package web

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
)

//go:embed templates/*.html
var templateFS embed.FS

type Handler struct {
	db       *db.DB
	sessions *auth.SessionManager
	hostname string
	login    *template.Template            // standalone pages
	pages    map[string]*template.Template // layout-composed pages
}

func NewHandler(database *db.DB, sessions *auth.SessionManager, hostname string) *Handler {
	h := &Handler{
		db:       database,
		sessions: sessions,
		hostname: strings.TrimRight(hostname, "/"),
		login:    template.Must(template.ParseFS(templateFS, "templates/login.html")),
		pages:    map[string]*template.Template{},
	}
	for _, p := range []string{"files", "apikeys"} {
		h.pages[p] = template.Must(template.ParseFS(
			templateFS,
			"templates/layout.html",
			"templates/"+p+".html",
		))
	}
	return h
}

type layoutData struct {
	ActiveTab string
	Title     string
	User      *db.User
}

// Files

type fileRow struct {
	ID          string
	Slug        string
	Path        string // canonical path: /{id}/{slug}, or /{id} without a slug
	URL         string // absolute canonical URL, for the copy button
	ContentType string
	Size        string
	Public      bool
	CreatedAt   string
}

type filesData struct {
	layoutData
	Files      []fileRow
	Hostname   string
	Paged      bool   // true when showing a page other than the first
	NextCursor string // cursor for the next (older) page; empty on the last page
}

const filesPerPage = 50

// fileCursor encodes a row's keyset position for the ?after= parameter.
func fileCursor(f *db.File) string {
	return fmt.Sprintf("%d_%s", f.CreatedAt.UnixNano(), f.ID)
}

// parseFileCursor is the inverse of fileCursor. Malformed input reports false
// and the caller falls back to the first page.
func parseFileCursor(s string) (time.Time, string, bool) {
	nanos, id, ok := strings.Cut(s, "_")
	if !ok || id == "" {
		return time.Time{}, "", false
	}
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return time.Time{}, "", false
	}
	return time.Unix(0, n).UTC(), id, true
}

func (h *Handler) FilesPage(w http.ResponseWriter, r *http.Request) {
	user := h.requireUser(w, r)
	if user == nil {
		return
	}

	var afterTime time.Time
	var afterID string
	if c := r.URL.Query().Get("after"); c != "" {
		if t, id, ok := parseFileCursor(c); ok {
			afterTime, afterID = t, id
		}
	}

	// Fetch one row beyond the page to learn whether an older page exists.
	files, err := h.db.ListFiles(r.Context(), user.ID, filesPerPage+1, afterTime, afterID)
	if err != nil {
		log.Printf("listing files: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := filesData{
		layoutData: layoutData{ActiveTab: "files", Title: "Files", User: user},
		Hostname:   h.hostname,
		Paged:      afterID != "",
	}
	if len(files) > filesPerPage {
		files = files[:filesPerPage]
		data.NextCursor = fileCursor(&files[len(files)-1])
	}
	for _, f := range files {
		path := "/" + f.ID
		if f.Slug != "" {
			path += "/" + f.Slug
		}
		data.Files = append(data.Files, fileRow{
			ID:          f.ID,
			Slug:        f.Slug,
			Path:        path,
			URL:         h.hostname + path,
			ContentType: f.ContentType,
			Size:        formatSize(f.Size),
			Public:      f.Public,
			CreatedAt:   f.CreatedAt.Local().Format(time.DateTime),
		})
	}
	h.render(w, "files", data)
}

// API keys

type keyRow struct {
	ID        string
	Label     string
	Prefix    string
	CreatedAt string
}

type keysData struct {
	layoutData
	Keys []keyRow
}

func (h *Handler) KeysPage(w http.ResponseWriter, r *http.Request) {
	user := h.requireUser(w, r)
	if user == nil {
		return
	}

	keys, err := h.db.ListAPIKeys(r.Context(), user.ID)
	if err != nil {
		log.Printf("listing keys: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := keysData{layoutData: layoutData{ActiveTab: "keys", Title: "API Keys", User: user}}
	for _, k := range keys {
		data.Keys = append(data.Keys, keyRow{
			ID:        k.ID,
			Label:     k.Label,
			Prefix:    k.KeyPrefix,
			CreatedAt: k.CreatedAt.Local().Format(time.DateTime),
		})
	}
	h.render(w, "apikeys", data)
}

// Auth helpers

func (h *Handler) requireUser(w http.ResponseWriter, r *http.Request) *db.User {
	userID, ok := h.sessions.GetUserID(r)
	if !ok {
		h.renderStandalone(w, "login.html", nil)
		return nil
	}

	user, err := h.db.GetUserByID(r.Context(), userID)
	if err != nil {
		h.sessions.ClearSession(w)
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return nil
	}
	return user
}

// Render helpers

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	t, ok := h.pages[name]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout.html", data); err != nil {
		log.Printf("rendering %s: %v", name, err)
	}
}

func (h *Handler) renderStandalone(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.login.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("rendering %s: %v", name, err)
	}
}

func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
