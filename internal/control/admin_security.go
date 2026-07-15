package control

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/to-alan/vaultmesh/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type publicPasskey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

func (s *HTTPServer) sensitiveAuthenticationKey(r *http.Request) string {
	key := authClientKey(r)
	cookie, err := r.Cookie(s.adminAuth.cookieName())
	if err != nil || cookie.Value == "" {
		return key + ":session:unknown"
	}
	sum := sha256.Sum256([]byte(cookie.Value))
	return key + ":session:" + base64.RawURLEncoding.EncodeToString(sum[:8])
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
	if a.security.TOTPEnabled {
		if !s.allowAuthenticationAttempt(w, s.secondFactors, clientKey, now) {
			a.mu.Unlock()
			return
		}
		if err := a.verifySecondFactorLocked(r.Context(), input.Code, true, now); err != nil {
			a.mu.Unlock()
			if !errors.Is(err, store.ErrUnauthorized) {
				s.logger.Error("persist reauthentication second factor", "error", err)
				s.writeError(w, http.StatusInternalServerError, "internal_error", "identity confirmation could not be completed", nil)
				return
			}
			if s.recordAuthenticationFailure(w, s.secondFactors, clientKey, now) {
				return
			}
			s.writeError(w, http.StatusUnauthorized, "invalid_second_factor", "verification code is invalid", nil)
			return
		}
		s.secondFactors.recordSuccess(clientKey)
	}
	a.mu.Unlock()
	if !a.markRecentlyAuthenticated(r, now) {
		s.writeError(w, http.StatusUnauthorized, "unauthorized", "administrator login required", nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) changePassword(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	clientKey := s.sensitiveAuthenticationKey(r)
	if !s.allowAuthenticationAttempt(w, s.passwordLimits, clientKey, now) {
		return
	}
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
		if s.recordAuthenticationFailure(w, s.passwordLimits, clientKey, now) {
			return
		}
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect", nil)
		return
	}
	s.passwordLimits.recordSuccess(clientKey)
	if bcrypt.CompareHashAndPassword(a.account.PasswordHash, []byte(input.NewPassword)) == nil {
		a.mu.Unlock()
		s.writeError(w, http.StatusUnprocessableEntity, "password_unchanged", "new password must be different", nil)
		return
	}
	if a.security.TOTPEnabled {
		if !s.allowAuthenticationAttempt(w, s.secondFactors, clientKey, now) {
			a.mu.Unlock()
			return
		}
		if err := a.verifySecondFactorLocked(r.Context(), input.VerificationCode, true, now); err != nil {
			a.mu.Unlock()
			if !errors.Is(err, store.ErrUnauthorized) {
				s.logger.Error("persist password-change second factor", "error", err)
				s.writeError(w, http.StatusInternalServerError, "internal_error", "password could not be changed", nil)
				return
			}
			if s.recordAuthenticationFailure(w, s.secondFactors, clientKey, now) {
				return
			}
			s.writeError(w, http.StatusUnauthorized, "invalid_second_factor", "verification code is invalid", nil)
			return
		}
		s.secondFactors.recordSuccess(clientKey)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if err == nil {
		previousAccount := a.account
		a.account.PasswordHash = hash
		err = a.saveLocked(r.Context())
		if err != nil {
			a.account = previousAccount
		}
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

// TOTP and passkey handlers live in admin_totp.go and admin_passkey.go.
