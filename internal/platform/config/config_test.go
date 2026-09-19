package config

import (
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
		Env:         "test",
		HTTPAddr:    ":8080",
		DatabaseURL: "postgres://test",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}
