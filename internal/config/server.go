package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Server struct {
	ListenAddress string
	DatabaseURL   string
	AdminToken    string
	MasterKey     string
	WebDir        string
	AutoMigrate   bool
}

func LoadServer() (Server, error) {
	config := Server{
		ListenAddress: envOr("VAULTMESH_LISTEN", ":8080"),
		DatabaseURL:   strings.TrimSpace(os.Getenv("VAULTMESH_DATABASE_URL")),
		AdminToken:    strings.TrimSpace(os.Getenv("VAULTMESH_ADMIN_TOKEN")),
		MasterKey:     strings.TrimSpace(os.Getenv("VAULTMESH_MASTER_KEY")),
		WebDir:        envOr("VAULTMESH_WEB_DIR", "./web/dist"),
		AutoMigrate:   envBool("VAULTMESH_AUTO_MIGRATE", true),
	}
	if len(config.AdminToken) < 24 {
		return Server{}, fmt.Errorf("VAULTMESH_ADMIN_TOKEN must contain at least 24 characters")
	}
	if config.MasterKey == "" {
		return Server{}, fmt.Errorf("VAULTMESH_MASTER_KEY is required")
	}
	return config, nil
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
