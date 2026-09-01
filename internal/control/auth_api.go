package control

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/store"
)

func (s *HTTPServer) login(w http.ResponseWriter, r *http.Request) {
	clientKey := authClientKey(r)
	if !s.allowAuthenticationAttempt(w, s.passwordLimits, clientKey, time.Now()) {
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if !s.adminAuth.authenticate(strings.TrimSpace(input.Username), input.Password) {
		if s.recordAuthenticationFailure(w, s.passwordLimits, clientKey, time.Now()) {
			return
		}
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is incorrect", nil)
		return
	}
	s.passwordLimits.recordSuccess(clientKey)
	if s.adminAuth.totpEnabled() {
		token, err := s.adminAuth.createPendingMFA(time.Now())
		if err != nil {
			s.logger.Error("create pending MFA session", "error", err)
			s.writeError(w, http.StatusInternalServerError, "internal_error", "two-step login could not be started", nil)
			return
		}
		s.adminAuth.setMFACookie(w, token)
		s.writeJSON(w, http.StatusAccepted, map[string]any{"mfa_required": true})
		return
	}
	s.issueAdminSession(w)
}

func (s *HTTPServer) completeTOTPLogin(w http.ResponseWriter, r *http.Request) {
	clientKey := authClientKey(r)
	if !s.allowAuthenticationAttempt(w, s.secondFactors, clientKey, time.Now()) {
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if err := s.adminAuth.completePendingMFA(r.Context(), r, input.Code, time.Now()); err != nil {
		if !errors.Is(err, store.ErrUnauthorized) {
			s.logger.Error("persist login second factor", "error", err)
			s.writeError(w, http.StatusInternalServerError, "internal_error", "two-step login could not be completed", nil)
			return
		}
		if s.recordAuthenticationFailure(w, s.secondFactors, clientKey, time.Now()) {
			return
		}
		s.writeError(w, http.StatusUnauthorized, "invalid_second_factor", "verification code is invalid or the login attempt expired", nil)
		return
	}
	s.secondFactors.recordSuccess(clientKey)
	s.adminAuth.clearCookie(w, s.adminAuth.mfaCookieName(), s.adminAuth.cookieSecure)
	s.issueAdminSession(w)
}

func (s *HTTPServer) issueAdminSession(w http.ResponseWriter) {
	token, session, err := s.adminAuth.createSession(time.Now())
	if err != nil {
		s.logger.Error("create administrator session", "error", err)
		s.writeError(w, http.StatusInternalServerError, "internal_error", "administrator session could not be created", nil)
		return
	}
	s.adminAuth.setSessionCookie(w, token)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"username":   session.Username,
		"expires_at": session.ExpiresAt,
	})
}

func (s *HTTPServer) logout(w http.ResponseWriter, r *http.Request) {
	s.adminAuth.deleteSession(r)
	s.adminAuth.clearSessionCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) session(w http.ResponseWriter, r *http.Request) {
	session, _ := s.adminAuth.session(r, time.Now())
	s.writeJSON(w, http.StatusOK, map[string]any{
		"username":   session.Username,
		"expires_at": session.ExpiresAt,
	})
}

func (s *HTTPServer) allowAuthenticationAttempt(w http.ResponseWriter, limiter *authAttemptLimiter, clientKey string, now time.Time) bool {
	if retryAfter, blocked := limiter.retryAfter(clientKey, now); blocked {
		s.writeAuthenticationRateLimit(w, retryAfter)
		return false
	}
	return true
}

func (s *HTTPServer) recordAuthenticationFailure(w http.ResponseWriter, limiter *authAttemptLimiter, clientKey string, now time.Time) bool {
	if retryAfter, blocked := limiter.recordFailure(clientKey, now); blocked {
		s.writeAuthenticationRateLimit(w, retryAfter)
		return true
	}
	return false
}

func (s *HTTPServer) writeAuthenticationRateLimit(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	s.writeError(w, http.StatusTooManyRequests, "rate_limited", "too many authentication attempts; retry later", nil)
}
