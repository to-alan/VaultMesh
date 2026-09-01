package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/to-alan/vaultmesh/internal/domain"
)

type Server struct {
	ListenAddress     string
	DatabaseURL       string
	AdminUsername     string
	AdminPassword     string
	MasterKey         string
	AllowedOrigins    []string
	CookieSecure      bool
	CookieSameSite    string
	WebAuthnRPID      string
	WebAuthnRPName    string
	WebAuthnRPOrigins []string
	AutoMigrate       bool
	Retention         domain.DataRetention
	// PublicAPIURL mirrors VAULTMESH_PUBLIC_API_URL so the server can advise
	// the web console and validate its own TLS posture.
	PublicAPIURL string
	// HTTPSReady reports whether TLS is considered configured. It derives
	// from the public API URL scheme and can be forced with
	// VAULTMESH_HTTPS_ENABLED for deployments that terminate TLS elsewhere.
	HTTPSReady bool
}

func LoadServer() (Server, error) {
	cookieSecure, err := envBool("VAULTMESH_COOKIE_SECURE", false)
	if err != nil {
		return Server{}, err
	}
	autoMigrate, err := envBool("VAULTMESH_AUTO_MIGRATE", true)
	if err != nil {
		return Server{}, err
	}
	runsDays, err := envInt("VAULTMESH_RETENTION_RUNS_DAYS", 90)
	if err != nil {
		return Server{}, err
	}
	commandsDays, err := envInt("VAULTMESH_RETENTION_COMMANDS_DAYS", 30)
	if err != nil {
		return Server{}, err
	}
	deliveriesDays, err := envInt("VAULTMESH_RETENTION_DELIVERIES_DAYS", 90)
	if err != nil {
		return Server{}, err
	}
	incidentsDays, err := envInt("VAULTMESH_RETENTION_INCIDENTS_DAYS", 180)
	if err != nil {
		return Server{}, err
	}
	auditDays, err := envInt("VAULTMESH_RETENTION_AUDIT_EVENTS_DAYS", 365)
	if err != nil {
		return Server{}, err
	}
	config := Server{
		ListenAddress:     envOr("VAULTMESH_LISTEN", ":8080"),
		DatabaseURL:       strings.TrimSpace(os.Getenv("VAULTMESH_DATABASE_URL")),
		AdminUsername:     strings.TrimSpace(os.Getenv("VAULTMESH_ADMIN_USERNAME")),
		AdminPassword:     os.Getenv("VAULTMESH_ADMIN_PASSWORD"),
		MasterKey:         strings.TrimSpace(os.Getenv("VAULTMESH_MASTER_KEY")),
		AllowedOrigins:    splitList(os.Getenv("VAULTMESH_ALLOWED_ORIGINS")),
		CookieSecure:      cookieSecure,
		CookieSameSite:    strings.ToLower(envOr("VAULTMESH_COOKIE_SAME_SITE", "lax")),
		AutoMigrate:       autoMigrate,
		WebAuthnRPID:      strings.TrimSpace(os.Getenv("VAULTMESH_WEBAUTHN_RP_ID")),
		WebAuthnRPName:    envOr("VAULTMESH_WEBAUTHN_RP_NAME", "VaultMesh"),
		WebAuthnRPOrigins: splitList(os.Getenv("VAULTMESH_WEBAUTHN_RP_ORIGINS")),
		Retention: domain.DataRetention{
			RunsDays:        runsDays,
			CommandsDays:    commandsDays,
			DeliveriesDays:  deliveriesDays,
			IncidentsDays:   incidentsDays,
			AuditEventsDays: auditDays,
		},
	}
	if len(config.WebAuthnRPOrigins) == 0 {
		config.WebAuthnRPOrigins = append([]string(nil), config.AllowedOrigins...)
	}
	config.PublicAPIURL = strings.TrimSpace(os.Getenv("VAULTMESH_PUBLIC_API_URL"))
	httpsEnabled, err := envBool("VAULTMESH_HTTPS_ENABLED", false)
	if err != nil {
		return Server{}, err
	}
	config.HTTPSReady = httpsEnabled || strings.HasPrefix(config.PublicAPIURL, "https://")
	if config.WebAuthnRPID == "" && len(config.WebAuthnRPOrigins) > 0 {
		parsed, _ := url.Parse(config.WebAuthnRPOrigins[0])
		if parsed != nil {
			config.WebAuthnRPID = parsed.Hostname()
		}
	}
	if config.AdminUsername == "" {
		return Server{}, fmt.Errorf("VAULTMESH_ADMIN_USERNAME is required")
	}
	if len(config.AdminPassword) < 12 {
		return Server{}, fmt.Errorf("VAULTMESH_ADMIN_PASSWORD must contain at least 12 characters")
	}
	if len([]byte(config.AdminPassword)) > 72 {
		return Server{}, fmt.Errorf("VAULTMESH_ADMIN_PASSWORD must not exceed 72 bytes")
	}
	if config.MasterKey == "" {
		return Server{}, fmt.Errorf("VAULTMESH_MASTER_KEY is required")
	}
	switch config.CookieSameSite {
	case "lax", "strict":
	case "none":
		if !config.CookieSecure {
			return Server{}, fmt.Errorf("VAULTMESH_COOKIE_SAME_SITE=none requires VAULTMESH_COOKIE_SECURE=true")
		}
	default:
		return Server{}, fmt.Errorf("VAULTMESH_COOKIE_SAME_SITE must be lax, strict, or none")
	}
	for _, origin := range config.AllowedOrigins {
		if err := validateOrigin(origin); err != nil {
			return Server{}, fmt.Errorf("VAULTMESH_ALLOWED_ORIGINS contains %q: %w", origin, err)
		}
	}
	for _, origin := range config.WebAuthnRPOrigins {
		if err := validateOrigin(origin); err != nil {
			return Server{}, fmt.Errorf("VAULTMESH_WEBAUTHN_RP_ORIGINS contains %q: %w", origin, err)
		}
	}
	if strings.Contains(config.WebAuthnRPID, "://") || strings.ContainsAny(config.WebAuthnRPID, "/:") || net.ParseIP(config.WebAuthnRPID) != nil {
		return Server{}, fmt.Errorf("VAULTMESH_WEBAUTHN_RP_ID must be a domain name without scheme, path, or port; IP addresses are not valid WebAuthn RP IDs")
	}
	return config, nil
}

func validateOrigin(origin string) error {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("must be an absolute HTTP or HTTPS origin")
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must not contain credentials, a path, query, or fragment")
	}
	return nil
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

// envInt reads a non-negative day count. Zero disables pruning for the scope;
// negative values are rejected so typos cannot silently disable or invert
// retention semantics.
func envInt(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer number of days", key)
	}
	return parsed, nil
}
