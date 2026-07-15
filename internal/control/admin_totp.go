package control

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"image/png"
	"net/http"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/to-alan/vaultmesh/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func (s *HTTPServer) beginTOTP(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	clientKey := s.sensitiveAuthenticationKey(r)
	if !s.allowAuthenticationAttempt(w, s.passwordLimits, clientKey, now) {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if !s.adminAuth.verifyPassword(input.Password) {
		if s.recordAuthenticationFailure(w, s.passwordLimits, clientKey, now) {
			return
		}
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect", nil)
		return
	}
	s.passwordLimits.recordSuccess(clientKey)
	sessionToken, ok := s.adminAuth.sessionTokenSum(r)
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "unauthorized", "administrator login required", nil)
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
	s.adminAuth.removeExpiredLocked(now)
	if len(s.adminAuth.pendingTOTP) >= maxAdminSessions {
		removeOldestTOTPSetup(s.adminAuth.pendingTOTP)
	}
	s.adminAuth.pendingTOTP[sessionToken] = pendingTOTPSetup{Secret: key.Secret(), ExpiresAt: now.Add(10 * time.Minute)}
	s.adminAuth.mu.Unlock()
	s.writeJSON(w, http.StatusOK, map[string]string{
		"secret": key.Secret(), "uri": key.URL(),
		"qr_code": "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes()),
	})
}

func (s *HTTPServer) enableTOTP(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	clientKey := s.sensitiveAuthenticationKey(r)
	if !s.allowAuthenticationAttempt(w, s.secondFactors, clientKey, now) {
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	sessionToken, ok := s.adminAuth.sessionTokenSum(r)
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "unauthorized", "administrator login required", nil)
		return
	}
	a := s.adminAuth
	a.mu.Lock()
	setup, ok := a.pendingTOTP[sessionToken]
	if !ok || !setup.ExpiresAt.After(now) || setup.Attempts >= maxMFAAttempts {
		delete(a.pendingTOTP, sessionToken)
		a.mu.Unlock()
		s.writeError(w, http.StatusConflict, "totp_setup_expired", "start TOTP setup again", nil)
		return
	}
	matchedStep := int64(0)
	currentStep := now.Unix() / 30
	for _, step := range []int64{currentStep, currentStep - 1, currentStep + 1} {
		expected, err := totp.GenerateCode(setup.Secret, time.Unix(step*30, 0))
		if err == nil && constantStringEqual(expected, strings.TrimSpace(input.Code)) {
			matchedStep = step
			break
		}
	}
	if matchedStep == 0 {
		setup.Attempts++
		if setup.Attempts >= maxMFAAttempts {
			delete(a.pendingTOTP, sessionToken)
		} else {
			a.pendingTOTP[sessionToken] = setup
		}
		a.mu.Unlock()
		if s.recordAuthenticationFailure(w, s.secondFactors, clientKey, now) {
			return
		}
		s.writeError(w, http.StatusUnauthorized, "invalid_second_factor", "verification code is invalid", nil)
		return
	}
	codes, hashes, err := generateRecoveryCodes(10)
	if err != nil {
		a.mu.Unlock()
		s.writeError(w, http.StatusInternalServerError, "internal_error", "recovery codes could not be generated", nil)
		return
	}
	previousSecurity := cloneAdminSecurityData(a.security)
	a.security.TOTPSecret = setup.Secret
	a.security.TOTPEnabled = true
	a.security.LastTOTPStep = matchedStep
	a.security.RecoveryCodeHash = hashes
	if err := a.saveLocked(r.Context()); err != nil {
		a.security = previousSecurity
		a.mu.Unlock()
		s.logger.Error("enable TOTP", "error", err)
		s.writeError(w, http.StatusInternalServerError, "internal_error", "two-step verification could not be enabled", nil)
		return
	}
	delete(a.pendingTOTP, sessionToken)
	a.mu.Unlock()
	s.secondFactors.recordSuccess(clientKey)
	s.writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

func (s *HTTPServer) disableTOTP(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	clientKey := s.sensitiveAuthenticationKey(r)
	if !s.allowAuthenticationAttempt(w, s.passwordLimits, clientKey, now) {
		return
	}
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
		if s.recordAuthenticationFailure(w, s.passwordLimits, clientKey, now) {
			return
		}
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect", nil)
		return
	}
	s.passwordLimits.recordSuccess(clientKey)
	if !s.allowAuthenticationAttempt(w, s.secondFactors, clientKey, now) {
		a.mu.Unlock()
		return
	}
	if err := a.verifySecondFactorLocked(r.Context(), input.Code, true, now); err != nil {
		a.mu.Unlock()
		if !errors.Is(err, store.ErrUnauthorized) {
			s.logger.Error("persist disable-TOTP second factor", "error", err)
			s.writeError(w, http.StatusInternalServerError, "internal_error", "two-step verification could not be disabled", nil)
			return
		}
		if s.recordAuthenticationFailure(w, s.secondFactors, clientKey, now) {
			return
		}
		s.writeError(w, http.StatusUnauthorized, "invalid_second_factor", "verification code is invalid", nil)
		return
	}
	s.secondFactors.recordSuccess(clientKey)
	previousSecurity := cloneAdminSecurityData(a.security)
	a.security.TOTPSecret = ""
	a.security.TOTPEnabled = false
	a.security.LastTOTPStep = 0
	a.security.RecoveryCodeHash = nil
	err := a.saveLocked(r.Context())
	if err != nil {
		a.security = previousSecurity
	}
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
	now := time.Now()
	clientKey := s.sensitiveAuthenticationKey(r)
	if !s.allowAuthenticationAttempt(w, s.passwordLimits, clientKey, now) {
		return
	}
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
		if s.recordAuthenticationFailure(w, s.passwordLimits, clientKey, now) {
			return
		}
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect", nil)
		return
	}
	s.passwordLimits.recordSuccess(clientKey)
	if !s.allowAuthenticationAttempt(w, s.secondFactors, clientKey, now) {
		a.mu.Unlock()
		return
	}
	if err := a.verifySecondFactorLocked(r.Context(), input.Code, false, now); err != nil {
		a.mu.Unlock()
		if !errors.Is(err, store.ErrUnauthorized) {
			s.logger.Error("persist recovery-code second factor", "error", err)
			s.writeError(w, http.StatusInternalServerError, "internal_error", "recovery codes could not be regenerated", nil)
			return
		}
		if s.recordAuthenticationFailure(w, s.secondFactors, clientKey, now) {
			return
		}
		s.writeError(w, http.StatusUnauthorized, "invalid_second_factor", "an authenticator code is required", nil)
		return
	}
	s.secondFactors.recordSuccess(clientKey)
	codes, hashes, err := generateRecoveryCodes(10)
	if err == nil {
		previousSecurity := cloneAdminSecurityData(a.security)
		a.security.RecoveryCodeHash = hashes
		err = a.saveLocked(r.Context())
		if err != nil {
			a.security = previousSecurity
		}
	}
	a.mu.Unlock()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal_error", "recovery codes could not be regenerated", nil)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
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
