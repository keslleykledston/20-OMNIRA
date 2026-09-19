package config

import (
	"encoding/base64"
	"os"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.Env != "development" {
		t.Errorf("expected env=development, got %s", cfg.Env)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("expected HTTPAddr=:8080, got %s", cfg.HTTPAddr)
	}
}

func TestLoadFromEnv(t *testing.T) {
	os.Setenv("OMNIRA_ENV", "production")
	os.Setenv("OMNIRA_HTTP_ADDR", ":9000")
	defer func() {
		os.Unsetenv("OMNIRA_ENV")
		os.Unsetenv("OMNIRA_HTTP_ADDR")
	}()

	cfg := Load()
	if cfg.Env != "production" {
		t.Errorf("expected env=production, got %s", cfg.Env)
	}
	if cfg.HTTPAddr != ":9000" {
		t.Errorf("expected HTTPAddr=:9000, got %s", cfg.HTTPAddr)
	}
}

func TestValidateMissingDatabaseURL(t *testing.T) {
	cfg := &Config{Env: "test", HTTPAddr: ":8080", DatabaseURL: ""}
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for missing DatabaseURL")
	}
}

func TestValidateValidConfig(t *testing.T) {
	cfg := &Config{
		Env:            "test",
		HTTPAddr:       ":8080",
		DatabaseURL:    "postgres://test",
		CredentialsKey: make([]byte, 32),
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestLoadRejectsMalformedCredentialKey(t *testing.T) {
	t.Setenv("OMNIRA_DATABASE_URL", "postgres://test")
	t.Setenv("OMNIRA_CREDENTIALS_KEY", "bad-base64")
	if err := Load().Validate(); err == nil {
		t.Fatal("malformed credential key accepted")
	}
	t.Setenv("OMNIRA_CREDENTIALS_KEY", base64.StdEncoding.EncodeToString(make([]byte, 31)))
	if err := Load().Validate(); err == nil {
		t.Fatal("short credential key accepted")
	}
	t.Setenv("OMNIRA_CREDENTIALS_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := Load().Validate(); err != nil {
		t.Fatalf("valid credential key rejected: %v", err)
	}
}

func TestValidateWAHAConfig(t *testing.T) {
	base := Config{
		Env:            "test",
		HTTPAddr:       ":8080",
		DatabaseURL:    "postgres://test",
		CredentialsKey: make([]byte, 32),
		WahaEnabled:    true,
		WahaBaseURL:    "http://waha:3000",
		WahaAPIKey:     "runtime-secret",
		WahaEngine:     "GOWS",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid WAHA config rejected: %v", err)
	}

	missingKey := base
	missingKey.WahaAPIKey = ""
	if err := missingKey.Validate(); err == nil {
		t.Fatal("missing WAHA API key accepted")
	}

	badEngine := base
	badEngine.WahaEngine = "UNKNOWN"
	if err := badEngine.Validate(); err == nil {
		t.Fatal("unsupported WAHA engine accepted")
	}
}

func TestValidateOIDCConfig(t *testing.T) {
	base := Config{
		Env:              "production",
		DatabaseURL:      "postgres://test",
		CredentialsKey:   make([]byte, 32),
		AuthMode:         "oidc",
		AuthIssuer:       "https://idp.example.com",
		AuthAudience:     "omnira",
		AuthClientID:     "client-id",
		AuthClientSecret: "runtime-secret",
		AuthRedirectURL:  "https://app.example.com/api/v1/auth/oidc/callback",
		AuthPostLoginURL: "/login?oidc=complete",
		AuthCookieSecure: true,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid OIDC config rejected: %v", err)
	}

	for name, mutate := range map[string]func(*Config){
		"insecure issuer":       func(c *Config) { c.AuthIssuer = "http://idp.example.com" },
		"insecure redirect":     func(c *Config) { c.AuthRedirectURL = "http://app.example.com/callback" },
		"external post-login":   func(c *Config) { c.AuthPostLoginURL = "https://evil.example.com" },
		"scheme-relative target": func(c *Config) { c.AuthPostLoginURL = "//evil.example.com" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("unsafe OIDC config accepted")
			}
		})
	}
}
