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
