package control

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"image/png"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp/totp"
	"github.com/to-alan/vaultmesh/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type publicPasskey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

func (s *HTTPServer) profile(w http.ResponseWriter, _ *http.Request) {
	s.adminAuth.mu.Lock()
	defer s.adminAuth.mu.Unlock()
	passkeys := make([]publicPasskey, 0, len(s.adminAuth.security.Passkeys))
	for _, item := range s.adminAuth.security.Passkeys {
		passkeys = append(passkeys, publicPasskey{
			ID: base64.RawURLEncoding.EncodeToString(item.Credential.ID), Name: item.Name,
			CreatedAt: item.CreatedAt, LastUsedAt: item.LastUsedAt,
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"username":                 s.adminAuth.account.Username,
		"totp_enabled":             s.adminAuth.security.TOTPEnabled,
		"recovery_codes_remaining": len(s.adminAuth.security.RecoveryCodeHash),
		"passkeys":                 passkeys,
		"webauthn_available":       s.adminAuth.webAuthn != nil,
		"webauthn_rp_id":           webAuthnRPID(s.adminAuth.webAuthn),
	})
}

func (s *HTTPServer) reauthenticate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	a := s.adminAuth
	a.mu.Lock()
	if bcrypt.CompareHashAndPassword(a.account.PasswordHash, []byte(input.Password)) != nil {
		a.mu.Unlock()
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect", nil)
		return
	}
	if a.security.TOTPEnabled {
		if err := a.verifySecondFactorLocked(r.Context(), input.Code, true, time.Now()); err != nil {
			a.mu.Unlock()
			s.writeError(w, http.StatusUnauthorized, "invalid_second_factor", "verification code is invalid", nil)
			return
		}
	}
	a.mu.Unlock()
	if !a.markRecentlyAuthenticated(r, time.Now()) {
		s.writeError(w, http.StatusUnauthorized, "unauthorized", "administrator login required", nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) changePassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CurrentPassword  string `json:"current_password"`
		NewPassword      string `json:"new_password"`
		VerificationCode string `json:"verification_code"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if err := validateAdminPassword(input.NewPassword); err != nil {
		s.writeError(w, http.StatusUnprocessableEntity, "invalid_password", err.Error(), nil)
		return
	}
	a := s.adminAuth
	a.mu.Lock()
	if bcrypt.CompareHashAndPassword(a.account.PasswordHash, []byte(input.CurrentPassword)) != nil {
		a.mu.Unlock()
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect", nil)
		return
	}
	if bcrypt.CompareHashAndPassword(a.account.PasswordHash, []byte(input.NewPassword)) == nil {
		a.mu.Unlock()
		s.writeError(w, http.StatusUnprocessableEntity, "password_unchanged", "new password must be different", nil)
		return
	}
	if a.security.TOTPEnabled {
		if err := a.verifySecondFactorLocked(r.Context(), input.VerificationCode, true, time.Now()); err != nil {
			a.mu.Unlock()
			s.writeError(w, http.StatusUnauthorized, "invalid_second_factor", "verification code is invalid", nil)
			return
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if err == nil {
		a.account.PasswordHash = hash
		err = a.saveLocked(r.Context())
	}
	a.mu.Unlock()
	if err != nil {
		s.logger.Error("change administrator password", "error", err)
		s.writeError(w, http.StatusInternalServerError, "internal_error", "password could not be changed", nil)
		return
	}
	a.revokeAllSessions()
	a.clearSessionCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) beginTOTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if !s.adminAuth.authenticate(s.adminAuth.account.Username, input.Password) {
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect", nil)
		return
	}
	s.adminAuth.mu.Lock()
	if s.adminAuth.security.TOTPEnabled {
		s.adminAuth.mu.Unlock()
		s.writeError(w, http.StatusConflict, "totp_already_enabled", "two-step verification is already enabled", nil)
		return
	}
	username := s.adminAuth.account.Username
	s.adminAuth.mu.Unlock()
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "VaultMesh", AccountName: username})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "TOTP secret could not be generated", nil)
		return
	}
	image, err := key.Image(240, 240)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "TOTP QR code could not be generated", nil)
		return
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image); err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "TOTP QR code could not be encoded", nil)
		return
	}
	s.adminAuth.mu.Lock()
	s.adminAuth.pendingTOTPKey = key.Secret()
	s.adminAuth.pendingTOTPAt = time.Now().Add(10 * time.Minute)
	s.adminAuth.mu.Unlock()
	s.writeJSON(w, http.StatusOK, map[string]string{
		"secret": key.Secret(), "uri": key.URL(),
		"qr_code": "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes()),
	})
}

func (s *HTTPServer) enableTOTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code string `json:"code"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	a := s.adminAuth
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingTOTPKey == "" || !a.pendingTOTPAt.After(time.Now()) {
		s.writeError(w, http.StatusConflict, "totp_setup_expired", "start TOTP setup again", nil)
		return
	}
	matchedStep := int64(0)
	currentStep := time.Now().Unix() / 30
	for _, step := range []int64{currentStep, currentStep - 1, currentStep + 1} {
		expected, err := totp.GenerateCode(a.pendingTOTPKey, time.Unix(step*30, 0))
		if err == nil && constantStringEqual(expected, strings.TrimSpace(input.Code)) {
			matchedStep = step
			break
		}
	}
	if matchedStep == 0 {
		s.writeError(w, http.StatusUnauthorized, "invalid_second_factor", "verification code is invalid", nil)
		return
	}
	codes, hashes, err := generateRecoveryCodes(10)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "recovery codes could not be generated", nil)
		return
	}
	a.security.TOTPSecret = a.pendingTOTPKey
	a.security.TOTPEnabled = true
	a.security.LastTOTPStep = matchedStep
	a.security.RecoveryCodeHash = hashes
	a.pendingTOTPKey = ""
	a.pendingTOTPAt = time.Time{}
	if err := a.saveLocked(r.Context()); err != nil {
		s.logger.Error("enable TOTP", "error", err)
		s.writeError(w, http.StatusInternalServerError, "internal_error", "two-step verification could not be enabled", nil)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

func (s *HTTPServer) disableTOTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	a := s.adminAuth
	a.mu.Lock()
	if bcrypt.CompareHashAndPassword(a.account.PasswordHash, []byte(input.Password)) != nil {
		a.mu.Unlock()
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect", nil)
		return
	}
	if err := a.verifySecondFactorLocked(r.Context(), input.Code, true, time.Now()); err != nil {
		a.mu.Unlock()
		s.writeError(w, http.StatusUnauthorized, "invalid_second_factor", "verification code is invalid", nil)
		return
	}
	a.security.TOTPSecret = ""
	a.security.TOTPEnabled = false
	a.security.LastTOTPStep = 0
	a.security.RecoveryCodeHash = nil
	err := a.saveLocked(r.Context())
	a.mu.Unlock()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "two-step verification could not be disabled", nil)
		return
	}
	a.revokeAllSessions()
	a.clearSessionCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	a := s.adminAuth
	a.mu.Lock()
	defer a.mu.Unlock()
	if bcrypt.CompareHashAndPassword(a.account.PasswordHash, []byte(input.Password)) != nil {
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect", nil)
		return
	}
	if err := a.verifySecondFactorLocked(r.Context(), input.Code, false, time.Now()); err != nil {
		s.writeError(w, http.StatusUnauthorized, "invalid_second_factor", "an authenticator code is required", nil)
		return
	}
	codes, hashes, err := generateRecoveryCodes(10)
	if err == nil {
		a.security.RecoveryCodeHash = hashes
		err = a.saveLocked(r.Context())
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "recovery codes could not be regenerated", nil)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

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
	s.adminAuth.security.Passkeys = append(s.adminAuth.security.Passkeys, storedPasskey{
		Name: ceremony.Name, Credential: *credential, CreatedAt: now,
	})
	err = s.adminAuth.saveLocked(r.Context())
	s.adminAuth.mu.Unlock()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "passkey could not be saved", nil)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"id": base64.RawURLEncoding.EncodeToString(credential.ID), "name": ceremony.Name, "created_at": now,
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
	now := time.Now().UTC()
	for index := range s.adminAuth.security.Passkeys {
		if subtle.ConstantTimeCompare(credential.ID, s.adminAuth.security.Passkeys[index].Credential.ID) == 1 {
			s.adminAuth.security.Passkeys[index].Credential = *credential
			s.adminAuth.security.Passkeys[index].LastUsedAt = &now
			break
		}
	}
	err = s.adminAuth.saveLocked(r.Context())
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
			a.security.Passkeys = append(a.security.Passkeys[:index], a.security.Passkeys[index+1:]...)
			if err := a.saveLocked(r.Context()); err != nil {
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

func generateRecoveryCodes(count int) ([]string, []string, error) {
	codes := make([]string, 0, count)
	hashes := make([]string, 0, count)
	for range count {
		value := make([]byte, 10)
		if _, err := rand.Read(value); err != nil {
			return nil, nil, err
		}
		raw := strings.TrimRight(base32.StdEncoding.EncodeToString(value), "=")
		code := raw[:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:]
		codes = append(codes, code)
		hashes = append(hashes, recoveryCodeHash(normalizeRecoveryCode(code)))
	}
	return codes, hashes, nil
}
