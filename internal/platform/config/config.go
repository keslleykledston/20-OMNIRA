package config

import (
	"encoding/base64"
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
	Env               string
	HTTPAddr          string
	DatabaseURL       string
	NatsURL           string
	OtelEndpoint      string
	AuthIssuer        string
	AuthAudience      string
	CredentialsKey    []byte
	credentialsKeyErr error
	WahaEnabled       bool
	WahaBaseURL       string
	WahaAPIKey        string
	WahaEngine        string
	MetaEnabled       bool
	MetaVerifyToken   string
	MetaAppSecret     string
	GracefulShutdown  int // segundos
}

func Load() *Config {
	keyRaw := os.Getenv("OMNIRA_CREDENTIALS_KEY")
	var key []byte
	var keyErr error
	if keyRaw == "" {
		keyErr = fmt.Errorf("OMNIRA_CREDENTIALS_KEY não pode estar vazio")
	} else {
		key, keyErr = base64.StdEncoding.DecodeString(keyRaw)
		if keyErr == nil && len(key) != 32 {
			keyErr = fmt.Errorf("OMNIRA_CREDENTIALS_KEY deve decodificar para 32 bytes")
		}
	}
	return &Config{
		Env:               getEnv("OMNIRA_ENV", "development"),
		HTTPAddr:          getEnv("OMNIRA_HTTP_ADDR", ":8080"),
		DatabaseURL:       getEnv("OMNIRA_DATABASE_URL", ""),
		NatsURL:           getEnv("OMNIRA_NATS_URL", "nats://localhost:4222"),
		OtelEndpoint:      getEnv("OMNIRA_OTEL_ENDPOINT", "http://localhost:4317"),
		AuthIssuer:        getEnv("OMNIRA_AUTH_ISSUER", "http://localhost:8080"),
		AuthAudience:      getEnv("OMNIRA_AUTH_AUDIENCE", "omnira"),
		CredentialsKey:    key,
		credentialsKeyErr: keyErr,
		WahaEnabled:       getEnv("OMNIRA_WAHA_ENABLED", "false") == "true",
		WahaBaseURL:       getEnv("OMNIRA_WAHA_BASE_URL", "http://waha:3000"),
		WahaAPIKey:        os.Getenv("OMNIRA_WAHA_API_KEY"),
		WahaEngine:        getEnv("OMNIRA_WAHA_ENGINE", "GOWS"),
		MetaEnabled:       getEnv("OMNIRA_META_ENABLED", "false") == "true",
		MetaVerifyToken:   os.Getenv("OMNIRA_META_VERIFY_TOKEN"),
		MetaAppSecret:     os.Getenv("OMNIRA_META_APP_SECRET"),
		GracefulShutdown:  getEnvInt("OMNIRA_GRACEFUL_SHUTDOWN", 30),
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
	if c.credentialsKeyErr != nil {
		return c.credentialsKeyErr
	}
	if len(c.CredentialsKey) != 32 {
		return fmt.Errorf("OMNIRA_CREDENTIALS_KEY deve decodificar para 32 bytes")
	}
	if c.WahaEnabled {
		if c.WahaBaseURL == "" || c.WahaAPIKey == "" {
			return fmt.Errorf("WAHA habilitado exige OMNIRA_WAHA_BASE_URL e OMNIRA_WAHA_API_KEY")
		}
		if c.WahaEngine != "GOWS" && c.WahaEngine != "NOWEB" && c.WahaEngine != "WEBJS" {
			return fmt.Errorf("OMNIRA_WAHA_ENGINE inválido")
		}
	}
	return nil
}
