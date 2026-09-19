package config

import (
	"fmt"
	"os"
	"strconv"
)

// Build info injected via ldflags
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

type Config struct {
	Env              string
	HTTPAddr         string
	DatabaseURL      string
	NatsURL          string
	OtelEndpoint     string
	AuthIssuer       string
	AuthAudience     string
	GracefulShutdown int // segundos
}

func Load() *Config {
	return &Config{
		Env:              getEnv("OMNIRA_ENV", "development"),
		HTTPAddr:         getEnv("OMNIRA_HTTP_ADDR", ":8080"),
		DatabaseURL:      getEnv("OMNIRA_DATABASE_URL", ""),
		NatsURL:          getEnv("OMNIRA_NATS_URL", "nats://localhost:4222"),
		OtelEndpoint:     getEnv("OMNIRA_OTEL_ENDPOINT", "http://localhost:4317"),
		AuthIssuer:       getEnv("OMNIRA_AUTH_ISSUER", "http://localhost:8080"),
		AuthAudience:     getEnv("OMNIRA_AUTH_AUDIENCE", "omnira"),
		GracefulShutdown: getEnvInt("OMNIRA_GRACEFUL_SHUTDOWN", 30),
	}
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

func (c *Config) Validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("OMNIRA_DATABASE_URL não pode estar vazio")
	}
	return nil
}
