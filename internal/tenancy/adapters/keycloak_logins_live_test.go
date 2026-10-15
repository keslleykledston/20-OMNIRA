package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/password"
)

// KeycloakLogins against a REAL Keycloak (scripts/test-keycloak-logins.sh starts a disposable one with the production image, realm file and
// realm changes). Skipped without it.
func TestKeycloakLoginsAgainstARealKeycloak(t *testing.T) {
	base, secret := os.Getenv("OMNIRA_TEST_KEYCLOAK_URL"), os.Getenv("OMNIRA_TEST_KEYCLOAK_PROVISIONER_SECRET")
	if base == "" || secret == "" {
		t.Skip("needs a disposable Keycloak: scripts/test-keycloak-logins.sh")
	}
	k := NewKeycloakLogins(base, "omnira", "omnira-provisioner", secret)
	ctx := context.Background()
	newPassword := func() string {
		p, err := password.Generate()
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	email := "convite-" + uuid.NewString()[:8] + "@kc.test"
	first := newPassword()
	if set, err := k.Prepare(ctx, email, first); err != nil || !set {
		t.Fatalf("Prepare on a new e-mail = %v, %v", set, err)
	}
	if got := signIn(t, base, email, first); got != "Account is not fully set up" {
		t.Fatalf("the e-mailed password must be the one Keycloak takes, with a new password required at the first sign-in; got %q", got)
	}
	if got := signIn(t, base, email, "Wrong1password"); got != "Invalid user credentials" {
		t.Fatalf("a wrong password = %q", got)
	}

	second := newPassword()
	if set, err := k.Prepare(ctx, email, second); err != nil || !set {
		t.Fatalf("resend on its own unused account = %v, %v", set, err)
	}
	if got := signIn(t, base, email, first); got != "Invalid user credentials" {
		t.Fatalf("after a resend the previous password stops working; got %q", got)
	}
	if got := signIn(t, base, email, second); got != "Account is not fully set up" {
		t.Fatalf("after a resend the new password works; got %q", got)
	}

	if err := k.Disable(ctx, email); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if got := signIn(t, base, email, second); got != "Account disabled" {
		t.Fatalf("a cancelled invitation turns its unused account off; got %q", got)
	}
	third := newPassword()
	if set, err := k.Prepare(ctx, email, third); err != nil || !set {
		t.Fatalf("a new invitation for the same e-mail = %v, %v", set, err)
	}
	if got := signIn(t, base, email, third); got != "Account is not fully set up" {
		t.Fatalf("a new invitation turns the account back on with its own password; got %q", got)
	}

	// Accounts created by hand (sistema2's case: a temporary password still pending; and one already in use) are never touched.
	for _, entry := range strings.Split(os.Getenv("OMNIRA_TEST_KEYCLOAK_MANUAL"), ",") {
		parts := strings.SplitN(entry, ":", 3)
		if len(parts) != 3 {
			continue
		}
		manual, own, want := parts[0], parts[1], parts[2]
		if set, err := k.Prepare(ctx, manual, newPassword()); err != nil || set {
			t.Fatalf("Prepare(%s) = %v, %v; want it untouched", manual, set, err)
		}
		if err := k.Disable(ctx, manual); err != nil {
			t.Fatalf("Disable(%s): %v", manual, err)
		}
		if got := signIn(t, base, manual, own); got != want {
			t.Fatalf("%s must still sign in with its own password as before (%q); got %q", manual, want, got)
		}
	}
}

// signIn tries the password grant of the realm's built-in admin-cli client and returns Keycloak's error description ("" = signed in).
func signIn(t *testing.T, base, user, pass string) string {
	t.Helper()
	form := url.Values{"grant_type": {"password"}, "client_id": {"admin-cli"}, "username": {user}, "password": {pass}}
	res, err := http.PostForm(strings.TrimRight(base, "/")+"/realms/omnira/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		return ""
	}
	var body struct {
		Description string `json:"error_description"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	return body.Description
}
