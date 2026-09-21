package authn

import (
	"net/http/httptest"
	"testing"
)

// return_to nunca pode virar um redirect aberto: só um path exato de convite
// passa. Qualquer coisa que aponte para fora do site, ou para uma rota que
// não seja a de aceite de convite, é descartada silenciosamente.
func TestSanitizeOIDCReturnTo(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"convite válido", "/invite/abcdEF0123456789xyz", "/invite/abcdEF0123456789xyz"},
		{"URL absoluta é rejeitada", "https://evil.example.com/invite/abcdEF0123456789xyz", ""},
		{"protocol-relative é rejeitado", "//evil.example.com/invite/abcdEF0123456789xyz", ""},
		{"outra rota do produto é rejeitada", "/settings/team", ""},
		{"token curto demais é rejeitado", "/invite/short", ""},
		{"vazio é rejeitado", "", ""},
		{"caminho com espaço é rejeitado", "/invite/abc def0123456789xyz", ""},
		{"traversal é rejeitado", "/invite/../admin", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeOIDCReturnTo(tc.in); got != tc.want {
				t.Errorf("sanitizeOIDCReturnTo(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStartSetsReturnToCookieOnlyWhenValid(t *testing.T) {
	h := &OIDCHandler{
		discovery: OIDCDiscovery{AuthorizationEndpoint: "https://idp.test/authorize"},
		clientID:  "client",
	}

	req := httptest.NewRequest("GET", "/api/v1/auth/oidc/start?return_to=/invite/abcdEF0123456789xyz", nil)
	rec := httptest.NewRecorder()
	h.Start(rec, req)
	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcReturnToCookie {
			found = true
			if c.Value != "/invite/abcdEF0123456789xyz" {
				t.Errorf("return_to cookie = %q", c.Value)
			}
		}
	}
	if !found {
		t.Fatal("valid return_to did not set the cookie")
	}

	req2 := httptest.NewRequest("GET", "/api/v1/auth/oidc/start?return_to=https://evil.example.com", nil)
	rec2 := httptest.NewRecorder()
	h.Start(rec2, req2)
	for _, c := range rec2.Result().Cookies() {
		if c.Name == oidcReturnToCookie {
			t.Fatalf("malicious return_to was accepted and cookied: %q", c.Value)
		}
	}
}
