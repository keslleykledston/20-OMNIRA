package config

import "testing"

func TestSMTPConfigValidation(t *testing.T) {
	base := func(mut func(*Config)) *Config {
		c := &Config{
			Env: "production", DatabaseURL: "postgres://x", CredentialsKey: make([]byte, 32),
			AuthMode: "oidc", AuthIssuer: "https://idp.test", AuthAudience: "omnira",
			AuthClientID: "id", AuthClientSecret: "secret",
			AuthRedirectURL: "https://app.test/cb", AuthPostLoginURL: "/login", AuthCookieSecure: true,
			SMTPHost: "smtp.test", SMTPPort: 587, SMTPFrom: "OMNIRA <no-reply@app.test>", SMTPTLS: "starttls",
			WebBaseURL: "https://app.test",
		}
		if mut != nil {
			mut(c)
		}
		return c
	}
	if err := base(nil).Validate(); err != nil {
		t.Fatalf("valid SMTP config rejected: %v", err)
	}
	if err := base(func(c *Config) { c.SMTPHost = ""; c.SMTPFrom = ""; c.WebBaseURL = "" }).Validate(); err != nil {
		t.Fatalf("no SMTP at all must be valid (delivery simply unavailable): %v", err)
	}
	for name, mut := range map[string]func(*Config){
		"missing from":          func(c *Config) { c.SMTPFrom = "" },
		"missing web base url":  func(c *Config) { c.WebBaseURL = "" },
		"relative web base url": func(c *Config) { c.WebBaseURL = "/app" },
		"http web base in prod": func(c *Config) { c.WebBaseURL = "http://app.test" },
		"tls none in prod":      func(c *Config) { c.SMTPTLS = "none" },
		"unknown tls mode":      func(c *Config) { c.SMTPTLS = "maybe" },
	} {
		if err := base(mut).Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	// dev/lab may use plaintext SMTP (Mailpit) and an http frontend
	if err := base(func(c *Config) { c.Env = "development"; c.SMTPTLS = "none"; c.WebBaseURL = "http://localhost:3000" }).Validate(); err != nil {
		t.Errorf("dev with Mailpit-style config rejected: %v", err)
	}
}
