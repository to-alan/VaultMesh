package control

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	localSessionCookie  = "vaultmesh_admin_session"
	secureSessionCookie = "__Host-vaultmesh_admin_session"
	defaultSessionTTL   = 12 * time.Hour
	maxAdminSessions    = 128
)

type AdminAuthConfig struct {
	Username     string
	Password     string
	CookieSecure bool
	SessionTTL   time.Duration
}

type adminSession struct {
	Username  string
	ExpiresAt time.Time
}

type adminAuthenticator struct {
	username     string
	usernameSum  [32]byte
	passwordHash []byte
	cookieSecure bool
	sessionTTL   time.Duration

	mu       sync.Mutex
	sessions map[[32]byte]adminSession
}

func newAdminAuthenticator(config AdminAuthConfig) (*adminAuthenticator, error) {
	if config.Username == "" || config.Username != strings.TrimSpace(config.Username) {
		return nil, fmt.Errorf("administrator username is required and must not have surrounding whitespace")
	}
	if len(config.Password) < 12 || len([]byte(config.Password)) > 72 {
		return nil, fmt.Errorf("administrator password must contain 12 to 72 bytes")
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(config.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	if config.SessionTTL <= 0 {
		config.SessionTTL = defaultSessionTTL
	}
	return &adminAuthenticator{
		username:     config.Username,
		usernameSum:  sha256.Sum256([]byte(config.Username)),
		passwordHash: passwordHash,
		cookieSecure: config.CookieSecure,
		sessionTTL:   config.SessionTTL,
		sessions:     make(map[[32]byte]adminSession),
	}, nil
}

func (a *adminAuthenticator) authenticate(username, password string) bool {
	usernameSum := sha256.Sum256([]byte(username))
	validUsername := subtle.ConstantTimeCompare(usernameSum[:], a.usernameSum[:]) == 1
	validPassword := bcrypt.CompareHashAndPassword(a.passwordHash, []byte(password)) == nil
	return validUsername && validPassword
}

func (a *adminAuthenticator) createSession(now time.Time) (string, adminSession, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", adminSession{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	session := adminSession{Username: a.username, ExpiresAt: now.Add(a.sessionTTL)}

	a.mu.Lock()
	a.removeExpiredLocked(now)
	if len(a.sessions) >= maxAdminSessions {
		var oldestToken [32]byte
		var oldestExpiry time.Time
		for existingToken, existingSession := range a.sessions {
			if oldestExpiry.IsZero() || existingSession.ExpiresAt.Before(oldestExpiry) {
				oldestToken = existingToken
				oldestExpiry = existingSession.ExpiresAt
			}
		}
		delete(a.sessions, oldestToken)
	}
	a.sessions[sha256.Sum256([]byte(token))] = session
	a.mu.Unlock()
	return token, session, nil
}

func (a *adminAuthenticator) session(r *http.Request, now time.Time) (adminSession, bool) {
	cookie, err := r.Cookie(a.cookieName())
	if err != nil || cookie.Value == "" {
		return adminSession{}, false
	}
	tokenSum := sha256.Sum256([]byte(cookie.Value))
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[tokenSum]
	if !ok || !session.ExpiresAt.After(now) {
		delete(a.sessions, tokenSum)
		return adminSession{}, false
	}
	return session, true
}

func (a *adminAuthenticator) deleteSession(r *http.Request) {
	for _, name := range []string{localSessionCookie, secureSessionCookie} {
		cookie, err := r.Cookie(name)
		if err != nil || cookie.Value == "" {
			continue
		}
		a.mu.Lock()
		delete(a.sessions, sha256.Sum256([]byte(cookie.Value)))
		a.mu.Unlock()
	}
}

func (a *adminAuthenticator) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookieName(),
		Value:    token,
		Path:     "/",
		Secure:   a.cookieSecure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *adminAuthenticator) clearSessionCookies(w http.ResponseWriter) {
	for _, cookie := range []struct {
		name   string
		secure bool
	}{
		{name: localSessionCookie, secure: false},
		{name: secureSessionCookie, secure: true},
	} {
		http.SetCookie(w, &http.Cookie{
			Name:     cookie.name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Expires:  time.Unix(1, 0),
			Secure:   cookie.secure,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func (a *adminAuthenticator) cookieName() string {
	if a.cookieSecure {
		return secureSessionCookie
	}
	return localSessionCookie
}

func (a *adminAuthenticator) removeExpiredLocked(now time.Time) {
	for token, session := range a.sessions {
		if !session.ExpiresAt.After(now) {
			delete(a.sessions, token)
		}
	}
}
