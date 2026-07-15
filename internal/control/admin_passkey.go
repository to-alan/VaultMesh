package control

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/to-alan/vaultmesh/internal/store"
)

func (s *HTTPServer) beginPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	if s.adminAuth.webAuthn == nil {
		s.writeError(w, http.StatusServiceUnavailable, "webauthn_unavailable", "WebAuthn is not configured", nil)
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 80 {
		s.writeError(w, http.StatusUnprocessableEntity, "invalid_passkey_name", "passkey name must contain 1 to 80 characters", nil)
		return
	}
	if !s.adminAuth.recentlyAuthenticated(r, time.Now()) {
		s.writeError(w, http.StatusPreconditionRequired, "reauthentication_required", "confirm your identity before changing passkeys", nil)
		return
	}
	s.adminAuth.mu.Lock()
	if len(s.adminAuth.security.Passkeys) >= maxPasskeys {
		s.adminAuth.mu.Unlock()
		s.writeError(w, http.StatusConflict, "passkey_limit", "the passkey limit has been reached", nil)
		return
	}
	user := &adminWebAuthnUser{account: s.adminAuth.account, security: s.adminAuth.security}
	s.adminAuth.mu.Unlock()
	options, session, err := s.adminAuth.webAuthn.BeginRegistration(user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(webauthn.Credentials(user.WebAuthnCredentials()).CredentialDescriptors()),
	)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "webauthn_error", "passkey registration could not be started", nil)
		return
	}
	token, err := s.adminAuth.saveCeremony("register", input.Name, *session)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "passkey ceremony could not be stored", nil)
		return
	}
	s.adminAuth.setCeremonyCookie(w, token)
	s.writeJSON(w, http.StatusOK, options)
}

func (s *HTTPServer) finishPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	ceremony, err := s.adminAuth.takeCeremony(r, "register")
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "webauthn_session_expired", "passkey registration session has expired", nil)
		return
	}
	s.adminAuth.mu.Lock()
	user := &adminWebAuthnUser{account: s.adminAuth.account, security: s.adminAuth.security}
	s.adminAuth.mu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	credential, err := s.adminAuth.webAuthn.FinishRegistration(user, ceremony.Session, r)
	if err != nil {
		s.logger.Warn("passkey registration verification failed", "error", err)
		s.writeError(w, http.StatusBadRequest, "webauthn_verification_failed", "passkey registration could not be verified", nil)
		return
	}
	s.adminAuth.mu.Lock()
	if len(s.adminAuth.security.Passkeys) >= maxPasskeys {
		s.adminAuth.mu.Unlock()
		s.writeError(w, http.StatusConflict, "passkey_limit", "the passkey limit has been reached", nil)
		return
	}
	now := time.Now().UTC()
	previousSecurity := cloneAdminSecurityData(s.adminAuth.security)
	s.adminAuth.security.Passkeys = append(s.adminAuth.security.Passkeys, storedPasskey{
		Name: ceremony.Name, Credential: *credential, CreatedAt: now,
	})
	err = s.adminAuth.saveLocked(r.Context())
	if err != nil {
		s.adminAuth.security = previousSecurity
	}
	s.adminAuth.mu.Unlock()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "passkey could not be saved", nil)
		return
	}
	credentialID := base64.RawURLEncoding.EncodeToString(credential.ID)
	setAuditResourceID(w, credentialID)
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"id": credentialID, "name": ceremony.Name, "created_at": now,
	})
}

func (s *HTTPServer) beginPasskeyLogin(w http.ResponseWriter, _ *http.Request) {
	if s.adminAuth.webAuthn == nil {
		s.writeError(w, http.StatusServiceUnavailable, "webauthn_unavailable", "WebAuthn is not configured", nil)
		return
	}
	s.adminAuth.mu.Lock()
	hasPasskeys := len(s.adminAuth.security.Passkeys) > 0
	s.adminAuth.mu.Unlock()
	if !hasPasskeys {
		s.writeError(w, http.StatusConflict, "no_passkeys", "no passkeys have been registered", nil)
		return
	}
	options, session, err := s.adminAuth.webAuthn.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "webauthn_error", "passkey login could not be started", nil)
		return
	}
	token, err := s.adminAuth.saveCeremony("login", "", *session)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "passkey ceremony could not be stored", nil)
		return
	}
	s.adminAuth.setCeremonyCookie(w, token)
	s.writeJSON(w, http.StatusOK, options)
}

func (s *HTTPServer) finishPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	ceremony, err := s.adminAuth.takeCeremony(r, "login")
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "webauthn_session_expired", "passkey login session has expired", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		s.adminAuth.mu.Lock()
		defer s.adminAuth.mu.Unlock()
		if subtle.ConstantTimeCompare(userHandle, s.adminAuth.account.WebAuthnUserID) != 1 {
			return nil, store.ErrNotFound
		}
		found := false
		for _, passkey := range s.adminAuth.security.Passkeys {
			if subtle.ConstantTimeCompare(rawID, passkey.Credential.ID) == 1 {
				found = true
				break
			}
		}
		if !found {
			return nil, store.ErrNotFound
		}
		return &adminWebAuthnUser{account: s.adminAuth.account, security: s.adminAuth.security}, nil
	}
	_, credential, err := s.adminAuth.webAuthn.FinishPasskeyLogin(handler, ceremony.Session, r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "webauthn_verification_failed", "passkey could not be verified", nil)
		return
	}
	s.adminAuth.mu.Lock()
	previousSecurity := cloneAdminSecurityData(s.adminAuth.security)
	now := time.Now().UTC()
	for index := range s.adminAuth.security.Passkeys {
		if subtle.ConstantTimeCompare(credential.ID, s.adminAuth.security.Passkeys[index].Credential.ID) == 1 {
			s.adminAuth.security.Passkeys[index].Credential = *credential
			s.adminAuth.security.Passkeys[index].LastUsedAt = &now
			break
		}
	}
	err = s.adminAuth.saveLocked(r.Context())
	if err != nil {
		s.adminAuth.security = previousSecurity
	}
	s.adminAuth.mu.Unlock()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "passkey state could not be saved", nil)
		return
	}
	s.issueAdminSession(w)
}

func (s *HTTPServer) deletePasskey(w http.ResponseWriter, r *http.Request) {
	credentialID, err := base64.RawURLEncoding.DecodeString(r.PathValue("passkeyID"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_passkey_id", "passkey ID is invalid", nil)
		return
	}
	if !s.adminAuth.recentlyAuthenticated(r, time.Now()) {
		s.writeError(w, http.StatusPreconditionRequired, "reauthentication_required", "confirm your identity before changing passkeys", nil)
		return
	}
	a := s.adminAuth
	a.mu.Lock()
	defer a.mu.Unlock()
	for index, passkey := range a.security.Passkeys {
		if subtle.ConstantTimeCompare(credentialID, passkey.Credential.ID) == 1 {
			previousSecurity := cloneAdminSecurityData(a.security)
			a.security.Passkeys = append(a.security.Passkeys[:index], a.security.Passkeys[index+1:]...)
			if err := a.saveLocked(r.Context()); err != nil {
				a.security = previousSecurity
				s.writeError(w, http.StatusInternalServerError, "internal_error", "passkey could not be deleted", nil)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	s.writeError(w, http.StatusNotFound, "not_found", "passkey was not found", nil)
}

func webAuthnRPID(instance *webauthn.WebAuthn) string {
	if instance == nil || instance.Config == nil {
		return ""
	}
	return instance.Config.RPID
}

func (a *adminAuthenticator) saveCeremony(mode, name string, session webauthn.SessionData) (string, error) {
	token, tokenSum, err := randomOpaqueToken()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.removeExpiredLocked(now)
	if len(a.ceremonies) >= maxShortLivedSessions {
		var oldest [32]byte
		var expiry time.Time
		for key, value := range a.ceremonies {
			if expiry.IsZero() || value.ExpiresAt.Before(expiry) {
				oldest, expiry = key, value.ExpiresAt
			}
		}
		delete(a.ceremonies, oldest)
	}
	a.ceremonies[tokenSum] = webAuthnCeremony{Mode: mode, Name: name, Session: session, ExpiresAt: now.Add(shortLivedAuthTTL)}
	return token, nil
}

func (a *adminAuthenticator) takeCeremony(r *http.Request, mode string) (webAuthnCeremony, error) {
	cookie, err := r.Cookie(a.ceremonyCookieName())
	if err != nil || cookie.Value == "" {
		return webAuthnCeremony{}, store.ErrUnauthorized
	}
	tokenSum := sha256Sum(cookie.Value)
	a.mu.Lock()
	defer a.mu.Unlock()
	ceremony, ok := a.ceremonies[tokenSum]
	delete(a.ceremonies, tokenSum)
	if !ok || ceremony.Mode != mode || !ceremony.ExpiresAt.After(time.Now()) {
		return webAuthnCeremony{}, store.ErrUnauthorized
	}
	return ceremony, nil
}

func sha256Sum(value string) [32]byte {
	return sha256.Sum256([]byte(value))
}
