package config

import (
	"strings"
	"testing"
)

func TestLoadServerUsesUsernamePasswordAndStrictBooleanConfiguration(t *testing.T) {
	t.Setenv("VAULTMESH_ADMIN_USERNAME", "admin")
	t.Setenv("VAULTMESH_ADMIN_PASSWORD", "correct-horse-battery-staple")
	t.Setenv("VAULTMESH_MASTER_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	t.Setenv("VAULTMESH_COOKIE_SECURE", "true")
	t.Setenv("VAULTMESH_AUTO_MIGRATE", "false")
	t.Setenv("VAULTMESH_ALLOWED_ORIGINS", "https://vault.example.com")

	config, err := LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if config.AdminUsername != "admin" || config.AdminPassword != "correct-horse-battery-staple" {
		t.Fatalf("unexpected administrator credentials: %#v", config)
	}
	if !config.CookieSecure || config.AutoMigrate {
		t.Fatalf("unexpected boolean configuration: %#v", config)
	}
	if config.WebAuthnRPID != "vault.example.com" || len(config.WebAuthnRPOrigins) != 1 {
		t.Fatalf("WebAuthn defaults were not derived from the trusted origin: %#v", config)
	}

	t.Setenv("VAULTMESH_COOKIE_SECURE", "sometimes")
	if _, err := LoadServer(); err == nil {
		t.Fatal("expected invalid cookie security boolean to fail")
	}
}

func TestSplitListAndValidateOrigin(t *testing.T) {
	origins := splitList(" https://console.example.com, http://127.0.0.1:5173 ,, ")
	if len(origins) != 2 || origins[0] != "https://console.example.com" || origins[1] != "http://127.0.0.1:5173" {
		t.Fatalf("unexpected origins: %#v", origins)
	}
	for _, origin := range origins {
		if err := validateOrigin(origin); err != nil {
			t.Fatalf("expected %q to be valid: %v", origin, err)
		}
	}
	for _, origin := range []string{"*", "javascript:alert(1)", "https://example.com/", "https://example.com/path", "https://user@example.com"} {
		if err := validateOrigin(origin); err == nil {
			t.Fatalf("expected %q to be invalid", origin)
		}
	}
}

func TestLoadServerRejectsIPAddressWebAuthnRPID(t *testing.T) {
	t.Setenv("VAULTMESH_ADMIN_USERNAME", "admin")
	t.Setenv("VAULTMESH_ADMIN_PASSWORD", "correct-horse-battery-staple")
	t.Setenv("VAULTMESH_MASTER_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	t.Setenv("VAULTMESH_ALLOWED_ORIGINS", "http://127.0.0.1:5173")

	if _, err := LoadServer(); err == nil || !strings.Contains(err.Error(), "IP addresses are not valid") {
		t.Fatalf("expected an IP-address RP ID error, got %v", err)
	}
}

func TestLoadServerAllowsCrossSiteCookiesOnlyOverHTTPS(t *testing.T) {
	t.Setenv("VAULTMESH_ADMIN_USERNAME", "admin")
	t.Setenv("VAULTMESH_ADMIN_PASSWORD", "correct-horse-battery-staple")
	t.Setenv("VAULTMESH_MASTER_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	t.Setenv("VAULTMESH_ALLOWED_ORIGINS", "https://console.other-site.example")
	t.Setenv("VAULTMESH_COOKIE_SAME_SITE", "none")
	t.Setenv("VAULTMESH_COOKIE_SECURE", "true")

	config, err := LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if config.CookieSameSite != "none" || !config.CookieSecure {
		t.Fatalf("unexpected cross-site cookie configuration: %#v", config)
	}

	t.Setenv("VAULTMESH_COOKIE_SECURE", "false")
	if _, err := LoadServer(); err == nil || !strings.Contains(err.Error(), "requires VAULTMESH_COOKIE_SECURE=true") {
		t.Fatalf("expected insecure SameSite=None configuration to fail, got %v", err)
	}
}
