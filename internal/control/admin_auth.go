package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp/totp"
	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	localSessionCookie    = "vaultmesh_admin_session"
	secureSessionCookie   = "__Host-vaultmesh_admin_session"
	localMFACookie        = "vaultmesh_admin_mfa"
	secureMFACookie       = "__Host-vaultmesh_admin_mfa"
	localCeremonyCookie   = "vaultmesh_webauthn_ceremony"
	secureCeremonyCookie  = "__Host-vaultmesh_webauthn_ceremony"
	defaultSessionTTL     = 12 * time.Hour
	shortLivedAuthTTL     = 5 * time.Minute
	recentAuthTTL         = 10 * time.Minute
	maxAdminSessions      = 128
	maxShortLivedSessions = 64
	maxMFAAttempts        = 5
	maxPasskeys           = 12
	passwordMinBytes      = 12
	passwordMaxBytes      = 72
)

type AdminAuthConfig struct {
	Username          string
	Password          string
	CookieSecure      bool
	CookieSameSite    string
	SessionTTL        time.Duration
	WebAuthnRPID      string
	WebAuthnRPName    string
	WebAuthnRPOrigins []string
}

type adminSession struct {
	Username        string
	ExpiresAt       time.Time
	AuthenticatedAt time.Time
}

type pendingMFA struct {
	ExpiresAt time.Time
	Attempts  int
}

type pendingTOTPSetup struct {
	Secret    string
	ExpiresAt time.Time
	Attempts  int
}

type webAuthnCeremony struct {
	Mode      string
	Name      string
	Session   webauthn.SessionData
	ExpiresAt time.Time
}

type storedPasskey struct {
	Name       string              `json:"name"`
	Credential webauthn.Credential `json:"credential"`
	CreatedAt  time.Time           `json:"created_at"`
	LastUsedAt *time.Time          `json:"last_used_at,omitempty"`
}

type adminSecurityData struct {
	TOTPSecret       string          `json:"totp_secret,omitempty"`
	TOTPEnabled      bool            `json:"totp_enabled"`
	LastTOTPStep     int64           `json:"last_totp_step,omitempty"`
	RecoveryCodeHash []string        `json:"recovery_code_hashes,omitempty"`
	Passkeys         []storedPasskey `json:"passkeys,omitempty"`
}

type adminAuthenticator struct {
	service        *Service
	cookieSecure   bool
	cookieSameSite http.SameSite
	sessionTTL     time.Duration
	webAuthn       *webauthn.WebAuthn

	mu          sync.Mutex
	account     domain.AdminAccount
	security    adminSecurityData
	sessions    map[[32]byte]adminSession
	pendingMFA  map[[32]byte]pendingMFA
	pendingTOTP map[[32]byte]pendingTOTPSetup
	ceremonies  map[[32]byte]webAuthnCeremony
}

type adminWebAuthnUser struct {
	account  domain.AdminAccount
	security adminSecurityData
}

func (u *adminWebAuthnUser) WebAuthnID() []byte          { return u.account.WebAuthnUserID }
func (u *adminWebAuthnUser) WebAuthnName() string        { return u.account.Username }
func (u *adminWebAuthnUser) WebAuthnDisplayName() string { return u.account.Username }
func (u *adminWebAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	credentials := make([]webauthn.Credential, 0, len(u.security.Passkeys))
	for _, passkey := range u.security.Passkeys {
		credentials = append(credentials, passkey.Credential)
	}
	return credentials
}

func newAdminAuthenticator(ctx context.Context, service *Service, config AdminAuthConfig) (*adminAuthenticator, error) {
	if config.Username == "" || config.Username != strings.TrimSpace(config.Username) {
		return nil, fmt.Errorf("administrator username is required and must not have surrounding whitespace")
	}
	if err := validateAdminPassword(config.Password); err != nil {
		return nil, err
	}
	if config.SessionTTL <= 0 {
		config.SessionTTL = defaultSessionTTL
	}
	cookieSameSite, err := parseCookieSameSite(config.CookieSameSite)
	if err != nil {
		return nil, err
	}
	if cookieSameSite == http.SameSiteNoneMode && !config.CookieSecure {
		return nil, fmt.Errorf("SameSite=None administrator cookies require Secure")
	}
	authenticator := &adminAuthenticator{
		service:        service,
		cookieSecure:   config.CookieSecure,
		cookieSameSite: cookieSameSite,
		sessionTTL:     config.SessionTTL,
		sessions:       make(map[[32]byte]adminSession),
		pendingMFA:     make(map[[32]byte]pendingMFA),
		pendingTOTP:    make(map[[32]byte]pendingTOTPSetup),
		ceremonies:     make(map[[32]byte]webAuthnCeremony),
	}
	if config.WebAuthnRPID != "" || len(config.WebAuthnRPOrigins) > 0 {
		if config.WebAuthnRPID == "" || len(config.WebAuthnRPOrigins) == 0 {
			return nil, fmt.Errorf("WebAuthn RP ID and at least one RP origin must be configured together")
		}
		if config.WebAuthnRPName == "" {
			config.WebAuthnRPName = "VaultMesh"
		}
		instance, err := webauthn.New(&webauthn.Config{
			RPID:          config.WebAuthnRPID,
			RPDisplayName: config.WebAuthnRPName,
			RPOrigins:     config.WebAuthnRPOrigins,
			AuthenticatorSelection: protocol.AuthenticatorSelection{
				ResidentKey:      protocol.ResidentKeyRequirementRequired,
				UserVerification: protocol.VerificationRequired,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("configure WebAuthn: %w", err)
		}
		authenticator.webAuthn = instance
	}

	account, err := service.store.GetAdminAccount(ctx)
	if errors.Is(err, store.ErrNotFound) {
		passwordHash, hashErr := bcrypt.GenerateFromPassword([]byte(config.Password), bcrypt.DefaultCost)
		if hashErr != nil {
			return nil, hashErr
		}
		userID := make([]byte, 64)
		if _, err := rand.Read(userID); err != nil {
			return nil, fmt.Errorf("generate WebAuthn user handle: %w", err)
		}
		now := time.Now().UTC()
		account = domain.AdminAccount{
			Username: config.Username, PasswordHash: passwordHash, WebAuthnUserID: userID,
			CreatedAt: now, UpdatedAt: now,
		}
		authenticator.account = account
		if err := authenticator.saveLocked(ctx); err != nil {
			return nil, fmt.Errorf("bootstrap administrator: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("load administrator: %w", err)
	} else {
		authenticator.account = account
		plaintext, err := service.sealer.Open(account.SecurityData)
		if err != nil {
			return nil, fmt.Errorf("decrypt administrator security data: %w", err)
		}
		if err := json.Unmarshal(plaintext, &authenticator.security); err != nil {
			return nil, fmt.Errorf("decode administrator security data: %w", err)
		}
	}
	return authenticator, nil
}

func parseCookieSameSite(value string) (http.SameSite, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "lax":
		return http.SameSiteLaxMode, nil
	case "strict":
		return http.SameSiteStrictMode, nil
	case "none":
		return http.SameSiteNoneMode, nil
	default:
		return http.SameSiteDefaultMode, fmt.Errorf("administrator cookie SameSite must be lax, strict, or none")
	}
}

func validateAdminPassword(password string) error {
	if len([]byte(password)) < passwordMinBytes || len([]byte(password)) > passwordMaxBytes {
		return fmt.Errorf("administrator password must contain 12 to 72 bytes")
	}
	return nil
}

func (a *adminAuthenticator) authenticate(username, password string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	usernameSum := sha256.Sum256([]byte(username))
	storedUsernameSum := sha256.Sum256([]byte(a.account.Username))
	validUsername := subtle.ConstantTimeCompare(usernameSum[:], storedUsernameSum[:]) == 1
	validPassword := bcrypt.CompareHashAndPassword(a.account.PasswordHash, []byte(password)) == nil
	return validUsername && validPassword
}

func (a *adminAuthenticator) verifyPassword(password string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return bcrypt.CompareHashAndPassword(a.account.PasswordHash, []byte(password)) == nil
}

func (a *adminAuthenticator) totpEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.security.TOTPEnabled
}

func (a *adminAuthenticator) createSession(now time.Time) (string, adminSession, error) {
	token, tokenSum, err := randomOpaqueToken()
	if err != nil {
		return "", adminSession{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session := adminSession{Username: a.account.Username, ExpiresAt: now.Add(a.sessionTTL), AuthenticatedAt: now}
	a.removeExpiredLocked(now)
	if len(a.sessions) >= maxAdminSessions {
		removeOldestSession(a.sessions)
	}
	a.sessions[tokenSum] = session
	return token, session, nil
}

func (a *adminAuthenticator) createPendingMFA(now time.Time) (string, error) {
	token, tokenSum, err := randomOpaqueToken()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.removeExpiredLocked(now)
	if len(a.pendingMFA) >= maxShortLivedSessions {
		removeOldestMFA(a.pendingMFA)
	}
	a.pendingMFA[tokenSum] = pendingMFA{ExpiresAt: now.Add(shortLivedAuthTTL)}
	return token, nil
}

func (a *adminAuthenticator) completePendingMFA(ctx context.Context, r *http.Request, code string, now time.Time) error {
	cookie, err := r.Cookie(a.mfaCookieName())
	if err != nil || cookie.Value == "" {
		return store.ErrUnauthorized
	}
	tokenSum := sha256.Sum256([]byte(cookie.Value))
	a.mu.Lock()
	defer a.mu.Unlock()
	pending, ok := a.pendingMFA[tokenSum]
	if !ok || !pending.ExpiresAt.After(now) || pending.Attempts >= maxMFAAttempts {
		delete(a.pendingMFA, tokenSum)
		return store.ErrUnauthorized
	}
	pending.Attempts++
	a.pendingMFA[tokenSum] = pending
	if err := a.verifySecondFactorLocked(ctx, code, true, now); err != nil {
		if pending.Attempts >= maxMFAAttempts {
			delete(a.pendingMFA, tokenSum)
		}
		return err
	}
	delete(a.pendingMFA, tokenSum)
	return nil
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

func (a *adminAuthenticator) recentlyAuthenticated(r *http.Request, now time.Time) bool {
	cookie, err := r.Cookie(a.cookieName())
	if err != nil || cookie.Value == "" {
		return false
	}
	tokenSum := sha256.Sum256([]byte(cookie.Value))
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[tokenSum]
	return ok && session.ExpiresAt.After(now) && !session.AuthenticatedAt.IsZero() &&
		!session.AuthenticatedAt.After(now) && !now.After(session.AuthenticatedAt.Add(recentAuthTTL))
}

func (a *adminAuthenticator) markRecentlyAuthenticated(r *http.Request, now time.Time) bool {
	cookie, err := r.Cookie(a.cookieName())
	if err != nil || cookie.Value == "" {
		return false
	}
	tokenSum := sha256.Sum256([]byte(cookie.Value))
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[tokenSum]
	if !ok || !session.ExpiresAt.After(now) {
		return false
	}
	session.AuthenticatedAt = now
	a.sessions[tokenSum] = session
	return true
}

func (a *adminAuthenticator) deleteSession(r *http.Request) {
	for _, name := range []string{localSessionCookie, secureSessionCookie} {
		cookie, err := r.Cookie(name)
		if err != nil || cookie.Value == "" {
			continue
		}
		a.mu.Lock()
		tokenSum := sha256.Sum256([]byte(cookie.Value))
		delete(a.sessions, tokenSum)
		delete(a.pendingTOTP, tokenSum)
		a.mu.Unlock()
	}
}

func (a *adminAuthenticator) revokeAllSessions() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions = make(map[[32]byte]adminSession)
	a.pendingMFA = make(map[[32]byte]pendingMFA)
	a.pendingTOTP = make(map[[32]byte]pendingTOTPSetup)
	a.ceremonies = make(map[[32]byte]webAuthnCeremony)
}

func (a *adminAuthenticator) saveLocked(ctx context.Context) error {
	plaintext, err := json.Marshal(a.security)
	if err != nil {
		return err
	}
	sealed, err := a.service.sealer.Seal(plaintext)
	if err != nil {
		return err
	}
	a.account.SecurityData = sealed
	a.account.UpdatedAt = time.Now().UTC()
	return a.service.store.SaveAdminAccount(ctx, a.account)
}

func (a *adminAuthenticator) verifySecondFactorLocked(ctx context.Context, code string, allowRecovery bool, now time.Time) error {
	code = strings.TrimSpace(code)
	if !a.security.TOTPEnabled || code == "" {
		return store.ErrUnauthorized
	}
	currentStep := now.Unix() / 30
	for _, step := range []int64{currentStep, currentStep - 1, currentStep + 1} {
		if step <= a.security.LastTOTPStep {
			continue
		}
		expected, err := totp.GenerateCode(a.security.TOTPSecret, time.Unix(step*30, 0))
		if err == nil && constantStringEqual(expected, code) {
			previousStep := a.security.LastTOTPStep
			a.security.LastTOTPStep = step
			if err := a.saveLocked(ctx); err != nil {
				a.security.LastTOTPStep = previousStep
				return err
			}
			return nil
		}
	}
	if allowRecovery {
		normalized := normalizeRecoveryCode(code)
		candidate := recoveryCodeHash(normalized)
		for index, hash := range a.security.RecoveryCodeHash {
			if constantStringEqual(hash, candidate) {
				previousHashes := append([]string(nil), a.security.RecoveryCodeHash...)
				a.security.RecoveryCodeHash = append(a.security.RecoveryCodeHash[:index], a.security.RecoveryCodeHash[index+1:]...)
				if err := a.saveLocked(ctx); err != nil {
					a.security.RecoveryCodeHash = previousHashes
					return err
				}
				return nil
			}
		}
	}
	return store.ErrUnauthorized
}

func (a *adminAuthenticator) setSessionCookie(w http.ResponseWriter, token string) {
	a.setOpaqueCookie(w, a.cookieName(), token, a.sessionTTL)
}

func (a *adminAuthenticator) setMFACookie(w http.ResponseWriter, token string) {
	a.setOpaqueCookie(w, a.mfaCookieName(), token, shortLivedAuthTTL)
}

func (a *adminAuthenticator) setCeremonyCookie(w http.ResponseWriter, token string) {
	a.setOpaqueCookie(w, a.ceremonyCookieName(), token, shortLivedAuthTTL)
}

func (a *adminAuthenticator) setOpaqueCookie(w http.ResponseWriter, name, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/",
		Secure: a.cookieSecure, HttpOnly: true, SameSite: a.cookieSameSite,
	})
	_ = maxAge // Expiry is enforced by the server; browser cookies remain non-persistent.
}

func (a *adminAuthenticator) clearSessionCookies(w http.ResponseWriter) {
	for _, cookie := range []struct {
		name   string
		secure bool
	}{
		{name: localSessionCookie}, {name: secureSessionCookie, secure: true},
		{name: localMFACookie}, {name: secureMFACookie, secure: true},
		{name: localCeremonyCookie}, {name: secureCeremonyCookie, secure: true},
	} {
		http.SetCookie(w, &http.Cookie{
			Name: cookie.name, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0),
			Secure: cookie.secure, HttpOnly: true, SameSite: a.cookieSameSite,
		})
	}
}

func (a *adminAuthenticator) clearCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0),
		Secure: secure, HttpOnly: true, SameSite: a.cookieSameSite,
	})
}

func (a *adminAuthenticator) cookieName() string {
	if a.cookieSecure {
		return secureSessionCookie
	}
	return localSessionCookie
}
func (a *adminAuthenticator) mfaCookieName() string {
	if a.cookieSecure {
		return secureMFACookie
	}
	return localMFACookie
}
func (a *adminAuthenticator) ceremonyCookieName() string {
	if a.cookieSecure {
		return secureCeremonyCookie
	}
	return localCeremonyCookie
}

func (a *adminAuthenticator) removeExpiredLocked(now time.Time) {
	for token, session := range a.sessions {
		if !session.ExpiresAt.After(now) {
			delete(a.sessions, token)
		}
	}
	for token, session := range a.pendingMFA {
		if !session.ExpiresAt.After(now) {
			delete(a.pendingMFA, token)
		}
	}
	for token, setup := range a.pendingTOTP {
		if !setup.ExpiresAt.After(now) {
			delete(a.pendingTOTP, token)
		}
	}
	for token, session := range a.ceremonies {
		if !session.ExpiresAt.After(now) {
			delete(a.ceremonies, token)
		}
	}
}

func (a *adminAuthenticator) sessionTokenSum(r *http.Request) ([32]byte, bool) {
	cookie, err := r.Cookie(a.cookieName())
	if err != nil || cookie.Value == "" {
		return [32]byte{}, false
	}
	return sha256.Sum256([]byte(cookie.Value)), true
}

func cloneAdminSecurityData(value adminSecurityData) adminSecurityData {
	value.RecoveryCodeHash = append([]string(nil), value.RecoveryCodeHash...)
	value.Passkeys = append([]storedPasskey(nil), value.Passkeys...)
	return value
}

func randomOpaqueToken() (string, [32]byte, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", [32]byte{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	return token, sha256.Sum256([]byte(token)), nil
}

func constantStringEqual(left, right string) bool {
	leftSum, rightSum := sha256.Sum256([]byte(left)), sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftSum[:], rightSum[:]) == 1
}

func normalizeRecoveryCode(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}

func recoveryCodeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func removeOldestSession(sessions map[[32]byte]adminSession) {
	var oldest [32]byte
	var expiry time.Time
	for token, session := range sessions {
		if expiry.IsZero() || session.ExpiresAt.Before(expiry) {
			oldest, expiry = token, session.ExpiresAt
		}
	}
	delete(sessions, oldest)
}

func removeOldestMFA(sessions map[[32]byte]pendingMFA) {
	var oldest [32]byte
	var expiry time.Time
	for token, session := range sessions {
		if expiry.IsZero() || session.ExpiresAt.Before(expiry) {
			oldest, expiry = token, session.ExpiresAt
		}
	}
	delete(sessions, oldest)
}

func removeOldestTOTPSetup(setups map[[32]byte]pendingTOTPSetup) {
	var oldest [32]byte
	var expiry time.Time
	for token, setup := range setups {
		if expiry.IsZero() || setup.ExpiresAt.Before(expiry) {
			oldest, expiry = token, setup.ExpiresAt
		}
	}
	delete(setups, oldest)
}
