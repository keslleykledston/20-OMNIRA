package config

import "testing"

func oidcBase() *Config {
	return &Config{
		Env: "test", HTTPAddr: ":8080", DatabaseURL: "postgres://test", CredentialsKey: make([]byte, 32),
		AuthMode: "oidc", AuthIssuer: "https://idp.example/realms/r", AuthAudience: "omnira-web", AuthClientID: "omnira-web",
		AuthClientSecret: "s", AuthRedirectURL: "https://app.example/api/v1/auth/oidc/callback", AuthPostLoginURL: "/login?oidc=complete",
	}
}

func TestMobileAuthIsOffByDefaultAndNeedsAnAllowlist(t *testing.T) {
	c := oidcBase()
	if err := c.Validate(); err != nil || c.MobileAuthEnabled {
		t.Fatalf("default must be off and valid: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"no redirect allowlist": func(c *Config) { c.MobileAuthEnabled, c.MobileClientID = true, "omnira-mobile" },
		"same client as web": func(c *Config) {
			c.MobileAuthEnabled, c.MobileClientID, c.MobileRedirectURIs = true, "omnira-web", []string{"com.omnira.app:/cb"}
		},
		"wildcard": func(c *Config) {
			c.MobileAuthEnabled, c.MobileClientID, c.MobileRedirectURIs = true, "omnira-mobile", []string{"com.omnira.*:/cb"}
		},
		"fragment": func(c *Config) {
			c.MobileAuthEnabled, c.MobileClientID, c.MobileRedirectURIs = true, "omnira-mobile", []string{"com.omnira.app:/cb#x"}
		},
		"plain http remote": func(c *Config) {
			c.MobileAuthEnabled, c.MobileClientID, c.MobileRedirectURIs = true, "omnira-mobile", []string{"http://evil.example/cb"}
		},
		"not a URI": func(c *Config) {
			c.MobileAuthEnabled, c.MobileClientID, c.MobileRedirectURIs = true, "omnira-mobile", []string{"nope"}
		},
		"mock auth mode": func(c *Config) {
			c.AuthMode, c.MobileAuthEnabled, c.MobileClientID, c.MobileRedirectURIs = "mock", true, "omnira-mobile", []string{"com.omnira.app:/cb"}
		},
	} {
		c := oidcBase()
		mutate(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	ok := oidcBase()
	ok.MobileAuthEnabled, ok.MobileClientID = true, "omnira-mobile"
	ok.MobileRedirectURIs = []string{"com.omnira.app:/oauth2redirect", "https://app.example/.well-known/mobile-callback", "http://127.0.0.1:8123/cb"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid mobile config rejected: %v", err)
	}
}

func TestMobileRedirectListIsParsedFromTheEnvironment(t *testing.T) {
	t.Setenv("OMNIRA_AUTH_MOBILE_ENABLED", "true")
	t.Setenv("OMNIRA_AUTH_MOBILE_REDIRECT_URIS", " com.omnira.app:/oauth2redirect , ,https://app.example/cb ")
	c := Load()
	if !c.MobileAuthEnabled || c.MobileClientID != "omnira-mobile" || len(c.MobileRedirectURIs) != 2 || c.MobileRedirectURIs[1] != "https://app.example/cb" {
		t.Fatalf("%+v", c)
	}
}
