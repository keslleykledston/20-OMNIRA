package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
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
	ValkeyURL         string
	OtelEndpoint      string
	AuthIssuer        string
	AuthAudience      string
	AuthMode          string
	AuthClientID      string
	AuthClientSecret  string
	AuthRedirectURL   string
	AuthPostLoginURL  string
	AuthCookieSecure  bool
	DevAuthEnabled    bool
	CredentialsKey    []byte
	credentialsKeyErr error
	WahaEnabled       bool
	WahaBaseURL       string
	WahaAPIKey        string
	WahaEngine        string
	// MediaDir: where the media pipeline keeps files (ADR-0016). Empty disables the pipeline reader.
	MediaDir string
	// ClamAVAddr: clamd host:port used by the worker's antivirus stage.
	ClamAVAddr string
	// WhisperURL: the local speech-to-text server (omnira-whisper systemd unit). Empty disables transcription.
	WhisperURL string
	MetaEnabled       bool
	// FlowsEnabled turns on the Flow Builder API and runtime (ADR-0019). Off by default: nothing changes when unset.
	FlowsEnabled bool
	// FlowsAIEnabled lets Flow AI nodes call the platform's configured model (also needs OMNIRA_AI_* ready). Off by default:
	// AI nodes then take their error port.
	FlowsAIEnabled bool
	PublicBaseURL     string
	// WebBaseURL: host do frontend como o navegador enxerga (links de e-mail). Diferente
	// de PublicBaseURL, que é a URL server-to-server usada por webhooks.
	WebBaseURL        string
	SMTPHost          string
	SMTPPort          int
	SMTPUsername      string
	SMTPPassword      string
	SMTPFrom          string
	SMTPReplyTo       string
	SMTPTLS           string // starttls (padrão) | implicit | none (só dev)
	AllowPrivilegedDB bool
	GracefulShutdown  int // segundos
	SessionIdleTimeout int // segundos (padrão 7200 = 2h)

	// AI* (PRODUCT.7C0/7C1): deliberately NOT validated by Validate() below.
	// AI is an optional, best-effort subsystem — a misconfigured/incomplete
	// AI_* set must never fail the whole API's boot (PRODUCT.7C0 §4,
	// PRODUCT.7C1 §4). AIReady() is the single place that decides whether
	// the feature may actually run; callers (apps/api/cmd/omnira-api)
	// consult it and construct no generator at all when false, which is
	// what makes the AI HTTP endpoint fail closed by construction.
	AIEnabled       bool
	AIProvider      string
	AIModel         string
	AIAPIKey        string
	AITimeoutSeconds int
}

// AIReady reports whether enough configuration exists to actually construct
// a provider generator. False for any reason (disabled, unsupported
// provider, missing model/key) means the AI capability must not be wired —
// never a partially-configured attempt that could fail unpredictably at
// request time instead of being visibly absent at boot.
func (c *Config) AIReady() bool {
	if !c.AIEnabled {
		return false
	}
	if c.AIProvider != "openai" {
		return false
	}
	return strings.TrimSpace(c.AIModel) != "" && strings.TrimSpace(c.AIAPIKey) != ""
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
		ValkeyURL:         getEnv("OMNIRA_VALKEY_URL", "redis://localhost:6379/0"),
		OtelEndpoint:      getEnv("OMNIRA_OTEL_ENDPOINT", "http://localhost:4317"),
		AuthIssuer:        getEnv("OMNIRA_AUTH_ISSUER", "http://localhost:8080"),
		AuthAudience:      getEnv("OMNIRA_AUTH_AUDIENCE", "omnira"),
		AuthMode:          getEnv("OMNIRA_AUTH_MODE", "mock"),
		AuthClientID:      os.Getenv("OMNIRA_AUTH_CLIENT_ID"),
		AuthClientSecret:  os.Getenv("OMNIRA_AUTH_CLIENT_SECRET"),
		AuthRedirectURL:   os.Getenv("OMNIRA_AUTH_REDIRECT_URL"),
		AuthPostLoginURL:  getEnv("OMNIRA_AUTH_POST_LOGIN_URL", "/login?oidc=complete"),
		AuthCookieSecure:  getEnv("OMNIRA_AUTH_COOKIE_SECURE", "false") == "true",
		DevAuthEnabled:    getEnv("OMNIRA_DEV_AUTH_ENABLED", "false") == "true",
		CredentialsKey:    key,
		credentialsKeyErr: keyErr,
		WahaEnabled:       getEnv("OMNIRA_WAHA_ENABLED", "false") == "true",
		WahaBaseURL:       getEnv("OMNIRA_WAHA_BASE_URL", "http://waha:3000"),
		WahaAPIKey:        os.Getenv("OMNIRA_WAHA_API_KEY"),
		WahaEngine:        getEnv("OMNIRA_WAHA_ENGINE", "GOWS"),
		MediaDir:          os.Getenv("OMNIRA_MEDIA_DIR"),
		ClamAVAddr:        os.Getenv("OMNIRA_CLAMAV_ADDR"),
		WhisperURL:        os.Getenv("OMNIRA_WHISPER_URL"),
		MetaEnabled:       getEnv("OMNIRA_META_ENABLED", "false") == "true",
		FlowsEnabled:      getEnv("OMNIRA_FLOWS_ENABLED", "false") == "true",
		FlowsAIEnabled:    getEnv("OMNIRA_FLOWS_AI_ENABLED", "false") == "true",
		PublicBaseURL:     os.Getenv("OMNIRA_PUBLIC_BASE_URL"),
		WebBaseURL:        os.Getenv("OMNIRA_WEB_BASE_URL"),
		SMTPHost:          os.Getenv("OMNIRA_SMTP_HOST"),
		SMTPPort:          getEnvInt("OMNIRA_SMTP_PORT", 587),
		SMTPUsername:      os.Getenv("OMNIRA_SMTP_USERNAME"),
		SMTPPassword:      os.Getenv("OMNIRA_SMTP_PASSWORD"),
		SMTPFrom:          os.Getenv("OMNIRA_SMTP_FROM"),
		SMTPReplyTo:       os.Getenv("OMNIRA_SMTP_REPLY_TO"),
		SMTPTLS:           getEnv("OMNIRA_SMTP_TLS", "starttls"),
		AllowPrivilegedDB: getEnv("OMNIRA_ALLOW_PRIVILEGED_DB", "false") == "true",
		GracefulShutdown:  getEnvInt("OMNIRA_GRACEFUL_SHUTDOWN", 30),
		SessionIdleTimeout: getEnvInt("OMNIRA_SESSION_IDLE_TIMEOUT", 7200), // padrão: 2h

		AIEnabled:        getEnv("OMNIRA_AI_ENABLED", "false") == "true",
		AIProvider:       getEnv("OMNIRA_AI_PROVIDER", "openai"),
		AIModel:          os.Getenv("OMNIRA_AI_MODEL"),
		AIAPIKey:         os.Getenv("OMNIRA_AI_API_KEY"),
		AITimeoutSeconds: getEnvInt("OMNIRA_AI_TIMEOUT_SECONDS", 15),
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
	authMode := c.AuthMode
	if authMode == "" {
		authMode = "mock"
	}
	if authMode != "mock" && authMode != "oidc" {
		return fmt.Errorf("OMNIRA_AUTH_MODE deve ser mock ou oidc")
	}
	if c.Env == "staging" || c.Env == "production" {
		if authMode == "mock" {
			return fmt.Errorf("OMNIRA_AUTH_MODE=mock é proibido em staging/production; exigido: oidc")
		}
	}
	// Fail-closed: a flag ligada fora de um ambiente de desenvolvimento é um
	// deploy mal configurado. Recusar o boot torna isso visível na hora, em vez
	// de ignorar a flag em silêncio e deixar a dúvida sobre o que está ativo.
	if c.DevAuthEnabled && !DevAuthEnvAllowed(c.Env) {
		return fmt.Errorf("OMNIRA_DEV_AUTH_ENABLED=true é proibido em OMNIRA_ENV=%s", c.Env)
	}
	if authMode == "oidc" {
		if c.AuthIssuer == "" || c.AuthAudience == "" || c.AuthClientID == "" || c.AuthClientSecret == "" || c.AuthRedirectURL == "" {
			return fmt.Errorf("OIDC exige issuer, audience, client id, client secret e redirect URL")
		}
		if c.Env == "production" && !c.AuthCookieSecure {
			return fmt.Errorf("OIDC em produção exige OMNIRA_AUTH_COOKIE_SECURE=true")
		}
		issuerURL, err := url.Parse(c.AuthIssuer)
		if err != nil || issuerURL.Host == "" || (issuerURL.Scheme != "http" && issuerURL.Scheme != "https") {
			return fmt.Errorf("OMNIRA_AUTH_ISSUER deve ser uma URL HTTP(S) absoluta")
		}
		redirectURL, err := url.Parse(c.AuthRedirectURL)
		if err != nil || redirectURL.Host == "" || (redirectURL.Scheme != "http" && redirectURL.Scheme != "https") {
			return fmt.Errorf("OMNIRA_AUTH_REDIRECT_URL deve ser uma URL HTTP(S) absoluta")
		}
		if c.AuthPostLoginURL == "" || !strings.HasPrefix(c.AuthPostLoginURL, "/") || strings.HasPrefix(c.AuthPostLoginURL, "//") {
			return fmt.Errorf("OMNIRA_AUTH_POST_LOGIN_URL deve ser um caminho local iniciado por /, mas não //")
		}
		if c.Env == "production" && (issuerURL.Scheme != "https" || redirectURL.Scheme != "https") {
			return fmt.Errorf("OIDC em produção exige issuer e redirect URL HTTPS")
		}
	}
	if c.SMTPHost != "" {
		if c.SMTPFrom == "" {
			return fmt.Errorf("SMTP habilitado exige OMNIRA_SMTP_FROM")
		}
		if c.SMTPTLS != "starttls" && c.SMTPTLS != "implicit" && c.SMTPTLS != "none" {
			return fmt.Errorf("OMNIRA_SMTP_TLS deve ser starttls, implicit ou none")
		}
		if (c.Env == "staging" || c.Env == "production") && c.SMTPTLS == "none" {
			return fmt.Errorf("OMNIRA_SMTP_TLS=none é proibido em staging/production")
		}
		webURL, err := url.Parse(c.WebBaseURL)
		if err != nil || webURL.Host == "" || (webURL.Scheme != "http" && webURL.Scheme != "https") {
			return fmt.Errorf("SMTP habilitado exige OMNIRA_WEB_BASE_URL como URL HTTP(S) absoluta (host do frontend usado nos links de e-mail)")
		}
		if c.Env == "production" && webURL.Scheme != "https" {
			return fmt.Errorf("OMNIRA_WEB_BASE_URL deve ser HTTPS em produção")
		}
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

// DevAuthEnvAllowed diz se o ambiente admite o login de desenvolvimento.
//
// É metade da defesa: a outra é OMNIRA_DEV_AUTH_ENABLED. Só a combinação
// ambiente permitido + flag explícita habilita o dev auth, para que nem um
// OMNIRA_ENV errado nem uma flag esquecida bastem sozinhos.
func DevAuthEnvAllowed(env string) bool {
	switch env {
	case "local", "dev", "development", "lab", "test":
		return true
	default:
		return false
	}
}

// DevAuthActive é a resposta única sobre o dev auth estar ligado. Config,
// registro de rota e o endpoint de descoberta consultam esta função, para que
// não exista um caminho em que uma delas discorde das outras.
func (c *Config) DevAuthActive() bool {
	return c.DevAuthEnabled && DevAuthEnvAllowed(c.Env)
}
