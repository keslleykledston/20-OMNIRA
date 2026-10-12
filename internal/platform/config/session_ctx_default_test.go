package config

import "testing"

// Pre-gate review (Codex): the database ships in mode "off" and the deployment must not be able to run with real sign-in while it still trusts forgeable settings.
func TestSessionCtxEnforcementIsRequiredByDefaultWithRealSignIn(t *testing.T) {
	cases := []struct {
		env, mode string
		want      bool
	}{
		{"", "oidc", true},  // real people: required unless someone says otherwise
		{"", "mock", false}, // development with the mock login keeps it opt-in
		{"", "", false},
		{"true", "mock", true},
		{"false", "oidc", false}, // an explicit decision wins (a staged rollout)
		{"true", "oidc", true},
	}
	for _, c := range cases {
		t.Setenv("OMNIRA_SESSION_CTX_REQUIRE_ENFORCE", c.env)
		if got := sessionCtxRequireEnforce(c.mode); got != c.want {
			t.Errorf("env=%q auth mode=%q: %v, want %v", c.env, c.mode, got, c.want)
		}
	}
}
