package config

import (
	"testing"
)

func TestConfigFailClosedProduction(t *testing.T) {
	tests := []struct {
		name      string
		env       string
		authMode  string
		shouldErr bool
		errMsg    string
	}{
		{
			name:      "dev + mock",
			env:       "development",
			authMode:  "mock",
			shouldErr: false,
		},
		{
			name:      "dev + oidc",
			env:       "development",
			authMode:  "oidc",
			shouldErr: true, // will fail due to missing OIDC config
		},
		{
			name:      "staging + mock",
			env:       "staging",
			authMode:  "mock",
			shouldErr: true,
			errMsg:    "mock é proibido em staging/production",
		},
		{
			name:      "production + mock",
			env:       "production",
			authMode:  "mock",
			shouldErr: true,
			errMsg:    "mock é proibido em staging/production",
		},
		{
			name:      "invalid auth mode",
			env:       "development",
			authMode:  "invalid",
			shouldErr: true,
			errMsg:    "mock ou oidc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Env:            tt.env,
				AuthMode:       tt.authMode,
				DatabaseURL:    "postgres://user:pass@localhost/db",
				CredentialsKey: make([]byte, 32), // 32 zero bytes is valid
			}

			err := cfg.Validate()

			if tt.shouldErr {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				// Will fail due to missing OIDC config when authMode=oidc
				// That's OK for this test
				if err != nil && cfg.AuthMode == "oidc" {
					// Expected: missing OIDC config
					return
				}
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}

func TestConfigProductionHTTPS(t *testing.T) {
	tests := []struct {
		name       string
		issuer     string
		redirectURL string
		secure     bool
		shouldErr  bool
	}{
		{
			name:        "prod + https issuer + https redirect + secure cookie",
			issuer:      "https://idp.example.com/realms/omnira",
			redirectURL: "https://app.example.com/callback",
			secure:      true,
			shouldErr:   false,
		},
		{
			name:        "prod + http issuer",
			issuer:      "http://idp.example.com/realms/omnira",
			redirectURL: "https://app.example.com/callback",
			secure:      true,
			shouldErr:   true,
		},
		{
			name:        "prod + https issuer + http redirect",
			issuer:      "https://idp.example.com/realms/omnira",
			redirectURL: "http://app.example.com/callback",
			secure:      true,
			shouldErr:   true,
		},
		{
			name:        "prod + https + no secure cookie",
			issuer:      "https://idp.example.com/realms/omnira",
			redirectURL: "https://app.example.com/callback",
			secure:      false,
			shouldErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Env:              "production",
				AuthMode:         "oidc",
				DatabaseURL:      "postgres://user:pass@localhost/db",
				CredentialsKey:   make([]byte, 32),
				AuthIssuer:       tt.issuer,
				AuthRedirectURL:  tt.redirectURL,
				AuthPostLoginURL: "/login?oidc=complete",
				AuthAudience:     "app",
				AuthClientID:     "client",
				AuthClientSecret: "secret",
				AuthCookieSecure: tt.secure,
			}

			err := cfg.Validate()

			if tt.shouldErr {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
