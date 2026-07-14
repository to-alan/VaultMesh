package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Server struct {
	ListenAddress  string
	DatabaseURL    string
	AdminToken     string
	MasterKey      string
	AllowedOrigins []string
	AutoMigrate    bool
}

func LoadServer() (Server, error) {
	config := Server{
		ListenAddress:  envOr("VAULTMESH_LISTEN", ":8080"),
		DatabaseURL:    strings.TrimSpace(os.Getenv("VAULTMESH_DATABASE_URL")),
		AdminToken:     strings.TrimSpace(os.Getenv("VAULTMESH_ADMIN_TOKEN")),
		MasterKey:      strings.TrimSpace(os.Getenv("VAULTMESH_MASTER_KEY")),
		AllowedOrigins: splitList(os.Getenv("VAULTMESH_ALLOWED_ORIGINS")),
		AutoMigrate:    envBool("VAULTMESH_AUTO_MIGRATE", true),
	}
	if len(config.AdminToken) < 24 {
		return Server{}, fmt.Errorf("VAULTMESH_ADMIN_TOKEN must contain at least 24 characters")
	}
	if config.MasterKey == "" {
		return Server{}, fmt.Errorf("VAULTMESH_MASTER_KEY is required")
	}
	for _, origin := range config.AllowedOrigins {
		if err := validateOrigin(origin); err != nil {
			return Server{}, fmt.Errorf("VAULTMESH_ALLOWED_ORIGINS contains %q: %w", origin, err)
		}
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

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
