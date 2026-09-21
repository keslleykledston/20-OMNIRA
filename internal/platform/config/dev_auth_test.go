package config

import "testing"

// O dev auth exige ambiente permitido E flag explícita. Nenhum dos dois sozinho
// basta, e ligá-lo onde não é permitido impede o boot em vez de ser ignorado em
// silêncio — um deploy mal configurado precisa falhar na cara, não ficar num
// estado que ninguém sabe descrever.
func TestDevAuthActive(t *testing.T) {
	for _, tc := range []struct {
		env     string
		flag    bool
		want    bool
		because string
	}{
		{"production", false, false, "produção nunca tem dev auth"},
		{"staging", false, false, "staging nunca tem dev auth"},
		{"lab", false, false, "ambiente permitido não basta sem a flag"},
		{"development", false, false, "ambiente permitido não basta sem a flag"},
		{"lab", true, true, "ambiente permitido + flag"},
		{"development", true, true, "ambiente permitido + flag"},
		{"local", true, true, "ambiente permitido + flag"},
		{"test", true, true, "ambiente permitido + flag"},
		{"production", true, false, "a flag não habilita fora dos ambientes permitidos"},
	} {
		c := &Config{Env: tc.env, DevAuthEnabled: tc.flag}
		if got := c.DevAuthActive(); got != tc.want {
			t.Errorf("env=%s flag=%v: DevAuthActive()=%v, queria %v (%s)", tc.env, tc.flag, got, tc.want, tc.because)
		}
	}
}

func TestDevAuthFlagRefusesBootOutsideDevEnvironments(t *testing.T) {
	base := func(env string, flag bool) *Config {
		return &Config{
			Env: env, DatabaseURL: "postgres://x", CredentialsKey: make([]byte, 32),
			AuthMode: "oidc", AuthIssuer: "https://idp.test", AuthAudience: "omnira",
			AuthClientID: "id", AuthClientSecret: "secret",
			AuthRedirectURL: "https://app.test/cb", AuthPostLoginURL: "/login",
			AuthCookieSecure: true, DevAuthEnabled: flag,
		}
	}

	for _, env := range []string{"production", "staging"} {
		if err := base(env, true).Validate(); err == nil {
			t.Errorf("env=%s com OMNIRA_DEV_AUTH_ENABLED=true deveria recusar o boot", env)
		}
		if err := base(env, false).Validate(); err != nil {
			t.Errorf("env=%s sem a flag deveria subir normalmente: %v", env, err)
		}
	}

	for _, env := range []string{"lab", "development"} {
		if err := base(env, true).Validate(); err != nil {
			t.Errorf("env=%s com a flag deveria subir: %v", env, err)
		}
	}
}

func TestDevAuthEnvAllowed(t *testing.T) {
	for _, env := range []string{"local", "dev", "development", "lab", "test"} {
		if !DevAuthEnvAllowed(env) {
			t.Errorf("%s deveria ser ambiente de desenvolvimento", env)
		}
	}
	for _, env := range []string{"production", "staging", "prod", "", "Production"} {
		if DevAuthEnvAllowed(env) {
			t.Errorf("%s não deveria permitir dev auth", env)
		}
	}
}
