package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/stashysh/stashy/internal/db"
)

type OAuthHandler struct {
	config         *oauth2.Config
	db             *db.DB
	sessions       *SessionManager
	allowedDomains map[string]bool
}

func NewOAuthHandler(clientID, clientSecret, redirectURL string, database *db.DB, sessions *SessionManager, allowedDomains []string) *OAuthHandler {
	domains := make(map[string]bool, len(allowedDomains))
	for _, d := range allowedDomains {
		d = strings.TrimSpace(d)
		if d != "" {
			domains[strings.ToLower(d)] = true
		}
	}
	return &OAuthHandler{
		config: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Scopes:       []string{"openid", "email", "profile"},
			Endpoint:     google.Endpoint,
		},
		db:             database,
		sessions:       sessions,
		allowedDomains: domains,
	}
}

func (h *OAuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/google/login", h.handleLogin)
	mux.HandleFunc("GET /auth/google/callback", h.handleCallback)
	mux.HandleFunc("GET /auth/logout", h.handleLogout)
}

func (h *OAuthHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	state := generateState()
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		Path:     "/",
		MaxAge:   300,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, h.config.AuthCodeURL(state), http.StatusTemporaryRedirect)
}

func (h *OAuthHandler) handleCallback(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie("oauth_state")
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:   "oauth_state",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	token, err := h.config.Exchange(r.Context(), code)
	if err != nil {
		log.Printf("oauth exchange error: %v", err)
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return
	}

	userInfo, err := fetchGoogleUserInfo(r.Context(), h.config, token)
	if err != nil {
		log.Printf("fetching user info error: %v", err)
		http.Error(w, "failed to get user info", http.StatusInternalServerError)
		return
	}

	if !h.isAllowedEmail(userInfo.Email) {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}

	user, err := h.db.UpsertUser(r.Context(), userInfo.Sub, userInfo.Email, userInfo.Name)
	if err != nil {
		log.Printf("upsert user error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.sessions.SetSession(w, user.ID)
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

func (h *OAuthHandler) handleLogout(w http.ResponseWriter, r *http.Request) {
	h.sessions.ClearSession(w)
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

type googleUserInfo struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

func fetchGoogleUserInfo(ctx context.Context, config *oauth2.Config, token *oauth2.Token) (*googleUserInfo, error) {
	client := config.Client(ctx, token)
	resp, err := client.Get("https://openidconnect.googleapis.com/v1/userinfo")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var info googleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (h *OAuthHandler) isAllowedEmail(email string) bool {
	if len(h.allowedDomains) == 0 {
		return true
	}
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return false
	}
	return h.allowedDomains[strings.ToLower(parts[1])]
}

func generateState() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}
