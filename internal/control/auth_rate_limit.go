package control

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	authFailureLimit      = 5
	authFailureWindow     = 15 * time.Minute
	authInitialLockout    = time.Minute
	authMaximumLockout    = 15 * time.Minute
	authLimiterMaxEntries = 2048
)

// authAttemptLimiter is a bounded, process-local failure limiter. It is a
// defense-in-depth control; a reverse proxy should still enforce a distributed
// rate limit for multi-instance deployments.
type authAttemptLimiter struct {
	mu      sync.Mutex
	entries map[string]authAttemptState
}

type authAttemptState struct {
	windowStartedAt time.Time
	failures        int
	blockedUntil    time.Time
	lastSeenAt      time.Time
}

func newAuthAttemptLimiter() *authAttemptLimiter {
	return &authAttemptLimiter{entries: make(map[string]authAttemptState)}
}

func (l *authAttemptLimiter) retryAfter(key string, now time.Time) (time.Duration, bool) {
	key = normalizeAuthClientKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()

	state, ok := l.entries[key]
	if !ok {
		return 0, false
	}
	if !state.blockedUntil.IsZero() && now.Before(state.blockedUntil) {
		state.lastSeenAt = now
		l.entries[key] = state
		return state.blockedUntil.Sub(now), true
	}
	if now.Sub(state.windowStartedAt) >= authFailureWindow {
		delete(l.entries, key)
		return 0, false
	}
	state.lastSeenAt = now
	l.entries[key] = state
	return 0, false
}

func (l *authAttemptLimiter) recordFailure(key string, now time.Time) (time.Duration, bool) {
	key = normalizeAuthClientKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()

	state, ok := l.entries[key]
	if !ok || now.Sub(state.windowStartedAt) >= authFailureWindow {
		state = authAttemptState{windowStartedAt: now}
	}
	state.failures++
	state.lastSeenAt = now
	if state.failures >= authFailureLimit {
		lockout := authInitialLockout
		for attempt := authFailureLimit; attempt < state.failures && lockout < authMaximumLockout; attempt++ {
			lockout *= 2
		}
		if lockout > authMaximumLockout {
			lockout = authMaximumLockout
		}
		state.blockedUntil = now.Add(lockout)
	}
	if _, exists := l.entries[key]; !exists && len(l.entries) >= authLimiterMaxEntries {
		l.evictOldestLocked(now)
	}
	l.entries[key] = state
	if now.Before(state.blockedUntil) {
		return state.blockedUntil.Sub(now), true
	}
	return 0, false
}

func (l *authAttemptLimiter) recordSuccess(key string) {
	l.mu.Lock()
	delete(l.entries, normalizeAuthClientKey(key))
	l.mu.Unlock()
}

func (l *authAttemptLimiter) evictOldestLocked(now time.Time) {
	for key, state := range l.entries {
		if !now.Before(state.blockedUntil) && now.Sub(state.lastSeenAt) >= authFailureWindow {
			delete(l.entries, key)
		}
	}
	if len(l.entries) < authLimiterMaxEntries {
		return
	}
	var oldestKey string
	var oldestTime time.Time
	for key, state := range l.entries {
		if oldestKey == "" || state.lastSeenAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = state.lastSeenAt
		}
	}
	delete(l.entries, oldestKey)
}

func normalizeAuthClientKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "unknown"
	}
	return key
}

// authClientKey trusts forwarded client addresses only from a loopback peer.
// This supports a local reverse proxy without allowing an Internet client to
// bypass rate limits by spoofing X-Forwarded-For.
func authClientKey(r *http.Request) string {
	direct := remoteHost(r.RemoteAddr)
	directIP := net.ParseIP(direct)
	if directIP != nil && directIP.IsLoopback() {
		forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		var fallback net.IP
		for index := len(forwarded) - 1; index >= 0; index-- {
			if ip := net.ParseIP(strings.TrimSpace(forwarded[index])); ip != nil {
				fallback = ip
				if !ip.IsLoopback() {
					return ip.String()
				}
			}
		}
		if fallback != nil {
			return fallback.String()
		}
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
			return ip.String()
		}
	}
	if directIP != nil {
		return directIP.String()
	}
	return normalizeAuthClientKey(direct)
}

func remoteHost(remoteAddress string) string {
	remoteAddress = strings.TrimSpace(remoteAddress)
	if host, _, err := net.SplitHostPort(remoteAddress); err == nil {
		return host
	}
	return strings.Trim(remoteAddress, "[]")
}
