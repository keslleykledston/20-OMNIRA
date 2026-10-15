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

func TestOutboundMediaNeedsTheAntivirusAndTheMediaDirectory(t *testing.T) {
	c := oidcBase()
	c.OutboundMediaEnabled = true
	if err := c.Validate(); err == nil {
		t.Fatal("outbound media accepted without an antivirus or a media directory")
	}
	c.MediaDir = "/var/lib/omnira/media"
	if err := c.Validate(); err == nil {
		t.Fatal("outbound media accepted without an antivirus")
	}
	c.ClamAVAddr = "clamav:3310"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid outbound media config rejected: %v", err)
	}
	t.Setenv("OMNIRA_OUTBOUND_MEDIA_ENABLED", "true")
	if !Load().OutboundMediaEnabled {
		t.Fatal("flag not read from the environment")
	}
	t.Setenv("OMNIRA_OUTBOUND_MEDIA_ENABLED", "")
	if Load().OutboundMediaEnabled {
		t.Fatal("must be off by default")
	}
}

func TestInviteLoginProvisioningNeedsAKeycloakSetupOrDoesNotBoot(t *testing.T) {
	ready := func(c *Config) {
		c.InviteProvisionLogin, c.KeycloakAdminURL = true, "http://keycloak:8888"
		c.KeycloakProvisionerClientID, c.KeycloakProvisionerSecret = "omnira-provisioner", "s3cret"
	}
	for name, mutate := range map[string]func(*Config){
		"no secret":            func(c *Config) { ready(c); c.KeycloakProvisionerSecret = "" },
		"no client":            func(c *Config) { ready(c); c.KeycloakProvisionerClientID = "" },
		"mock sign-in":         func(c *Config) { ready(c); c.AuthMode = "mock" },
		"issuer without realm": func(c *Config) { ready(c); c.AuthIssuer = "https://idp.example/" },
		"admin URL not a URL":  func(c *Config) { ready(c); c.KeycloakAdminURL = "keycloak:8888" },
	} {
		c := oidcBase()
		mutate(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	c := oidcBase()
	ready(c)
	if err := c.Validate(); err != nil {
		t.Fatalf("complete setup rejected: %v", err)
	}
}

func TestSessionCeilingIsNeverShorterThanTheIdleTimeout(t *testing.T) {
	c := oidcBase()
	c.SessionIdleTimeout, c.SessionMaxLifetime = 21600, 3600
	if err := c.Validate(); err == nil {
		t.Fatal("a ceiling shorter than the idle timeout accepted")
	}
	for _, bad := range [][2]int{{-1, 36000}, {21600, -1}} {
		c.SessionIdleTimeout, c.SessionMaxLifetime = bad[0], bad[1]
		if err := c.Validate(); err == nil {
			t.Fatalf("negative lifetime %v accepted", bad)
		}
	}
	c.SessionIdleTimeout = 21600
	for _, max := range []int{0, 21600, 36000} {
		c.SessionMaxLifetime = max
		if err := c.Validate(); err != nil {
			t.Fatalf("max %d rejected: %v", max, err)
		}
	}
	t.Setenv("OMNIRA_SESSION_IDLE_TIMEOUT", "")
	t.Setenv("OMNIRA_SESSION_MAX_LIFETIME", "")
	if l := Load(); l.SessionIdleTimeout != 7200 || l.SessionMaxLifetime != 36000 {
		t.Fatalf("defaults: idle %d max %d", l.SessionIdleTimeout, l.SessionMaxLifetime)
	}
}
