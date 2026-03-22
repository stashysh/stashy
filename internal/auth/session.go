package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	sessionCookieName = "stashy_session"
	sessionMaxAge     = 30 * 24 * time.Hour // 30 days
)

type contextKey string

const userIDKey contextKey = "user_id"

type SessionManager struct {
	secret []byte
}

func NewSessionManager(secret string) *SessionManager {
	return &SessionManager{secret: []byte(secret)}
}

// SetSession creates a signed session cookie with the user's ID.
func (s *SessionManager) SetSession(w http.ResponseWriter, userID string) {
	signed := s.sign(userID)

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    signed,
		Path:     "/",
		MaxAge:   int(sessionMaxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSession removes the session cookie.
func (s *SessionManager) ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// GetUserID extracts and verifies the user ID from the session cookie.
func (s *SessionManager) GetUserID(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}

	value, ok := s.verify(cookie.Value)
	if !ok {
		return "", false
	}

	return value, true
}

// ContextWithUserID adds the user ID to the context.
func ContextWithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// UserIDFromContext retrieves the user ID from the context.
func UserIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDKey).(string)
	return id, ok
}

func (s *SessionManager) sign(value string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(value))
	sig := base64.URLEncoding.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%s.%s", value, sig)
}

func (s *SessionManager) verify(signed string) (string, bool) {
	parts := strings.SplitN(signed, ".", 2)
	if len(parts) != 2 {
		return "", false
	}

	value, sig := parts[0], parts[1]

	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(value))
	expected := base64.URLEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", false
	}

	return value, true
}
