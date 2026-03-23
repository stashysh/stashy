package auth

import (
	"net/http"
	"strings"

	"github.com/stashysh/stashy/internal/db"
)

// RequireAPIKey returns middleware that validates Bearer tokens against stored API keys.
func RequireAPIKey(database *db.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r)
			if token == "" {
				http.Error(w, "missing authorization", http.StatusUnauthorized)
				return
			}

			key, err := database.LookupAPIKey(r.Context(), token)
			if err != nil {
				http.Error(w, "invalid api key", http.StatusUnauthorized)
				return
			}

			ctx := ContextWithUserID(r.Context(), key.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}
