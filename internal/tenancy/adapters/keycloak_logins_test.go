package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeKeycloak answers the few admin REST calls KeycloakLogins makes, like Keycloak 25 with unmanaged attributes editable by admins.
type fakeKeycloak struct {
	mu         sync.Mutex
	users      map[string]map[string]any
	passwords  map[string]kcCredential
	tokenCalls int
	failAdmin  int
	puts       []string
}

const fakeSecret = "provisioner-s3cret"

func newFakeKeycloak(t *testing.T) (*fakeKeycloak, *KeycloakLogins) {
	t.Helper()
	f := &fakeKeycloak{users: map[string]map[string]any{}, passwords: map[string]kcCredential{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, NewKeycloakLogins(srv.URL+"/", "omnira", "omnira-provisioner", fakeSecret)
}

func (f *fakeKeycloak) addUser(id, email string, provisioned, pending, enabled bool) {
	attrs := map[string]any{}
	if provisioned {
		attrs[provisionedAttribute] = []any{"true"}
	}
	actions := []any{}
	if pending {
		actions = append(actions, "UPDATE_PASSWORD")
	}
	f.users[id] = map[string]any{"id": id, "username": email, "email": email, "enabled": enabled, "requiredActions": actions,
		"attributes": attrs, "firstName": "Keep", "lastName": "Me"}
}

func (f *fakeKeycloak) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/realms/omnira/protocol/openid-connect/token" {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "omnira-provisioner" || r.Form.Get("client_secret") != fakeSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.tokenCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 300})
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.failAdmin != 0 {
		w.WriteHeader(f.failAdmin)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/admin/realms/omnira")
	switch {
	case r.Method == http.MethodGet && path == "/users":
		out := []map[string]any{}
		for _, u := range f.users {
			if strings.EqualFold(u["email"].(string), r.URL.Query().Get("email")) {
				out = append(out, u)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && path == "/users":
		var in kcUser
		_ = json.NewDecoder(r.Body).Decode(&in)
		for _, u := range f.users {
			if strings.EqualFold(u["email"].(string), in.Email) {
				w.WriteHeader(http.StatusConflict)
				return
			}
		}
		id := "kc-" + in.Email
		attrs := map[string]any{}
		for k, v := range in.Attributes {
			attrs[k] = []any{v[0]}
		}
		actions := []any{}
		for _, a := range in.RequiredActions {
			actions = append(actions, a)
		}
		f.users[id] = map[string]any{"id": id, "username": in.Username, "email": in.Email, "emailVerified": in.EmailVerified,
			"enabled": in.Enabled, "requiredActions": actions, "attributes": attrs}
		if len(in.Credentials) > 0 {
			f.passwords[id] = in.Credentials[0]
		}
		w.Header().Set("Location", "/admin/realms/omnira/users/"+id)
		w.WriteHeader(http.StatusCreated)
	case strings.HasSuffix(path, "/reset-password") && r.Method == http.MethodPut:
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/users/"), "/reset-password")
		var c kcCredential
		_ = json.NewDecoder(r.Body).Decode(&c)
		f.passwords[id] = c
		f.puts = append(f.puts, "reset:"+id)
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(path, "/users/") && r.Method == http.MethodGet:
		u, ok := f.users[strings.TrimPrefix(path, "/users/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(u)
	case strings.HasPrefix(path, "/users/") && r.Method == http.MethodPut:
		id := strings.TrimPrefix(path, "/users/")
		var rep map[string]any
		_ = json.NewDecoder(r.Body).Decode(&rep)
		f.users[id] = rep
		f.puts = append(f.puts, "update:"+id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestKeycloakLoginsCreatesAMissingAccountWithATemporaryPassword(t *testing.T) {
	f, k := newFakeKeycloak(t)
	set, err := k.Prepare(context.Background(), "nova@empresa.com", "Temp0rary!x")
	if err != nil || !set {
		t.Fatalf("Prepare = %v, %v; want a created account", set, err)
	}
	u := f.users["kc-nova@empresa.com"]
	if u["enabled"] != true || u["emailVerified"] != true {
		t.Fatalf("created account must be enabled with a verified e-mail: %+v", u)
	}
	if got := u["attributes"].(map[string]any)[provisionedAttribute]; got == nil {
		t.Fatal("created account must carry the omnira_provisioned marker")
	}
	if c := f.passwords["kc-nova@empresa.com"]; c.Value != "Temp0rary!x" || !c.Temporary {
		t.Fatalf("password must be the invitation's, temporary: %+v", c)
	}
	if actions := u["requiredActions"].([]any); len(actions) != 1 || actions[0] != "UPDATE_PASSWORD" {
		t.Fatalf("the person must choose a new password at the first sign-in: %v", actions)
	}
}

func TestKeycloakLoginsNeverTouchesAnAccountItDidNotCreate(t *testing.T) {
	f, k := newFakeKeycloak(t)
	// sistema2's case: created by hand, still waiting for a new password, already a member somewhere.
	f.addUser("hand", "manual@empresa.com", false, true, true)
	f.addUser("used", "usada@empresa.com", false, false, true)
	for _, email := range []string{"manual@empresa.com", "usada@empresa.com"} {
		set, err := k.Prepare(context.Background(), email, "Temp0rary!x")
		if err != nil || set {
			t.Fatalf("Prepare(%s) = %v, %v; want the account left alone", email, set, err)
		}
		if err := k.Disable(context.Background(), email); err != nil {
			t.Fatalf("Disable(%s): %v", email, err)
		}
	}
	if len(f.puts) != 0 || len(f.passwords) != 0 {
		t.Fatalf("no write may reach an account OMNIRA did not create: puts=%v passwords=%v", f.puts, f.passwords)
	}
}

func TestKeycloakLoginsResetsAndReenablesOnlyItsOwnUnusedAccount(t *testing.T) {
	f, k := newFakeKeycloak(t)
	f.addUser("mine", "convidada@empresa.com", true, true, false)
	f.addUser("done", "jaentrou@empresa.com", true, false, true)

	set, err := k.Prepare(context.Background(), "convidada@empresa.com", "N0vaSenha!x")
	if err != nil || !set {
		t.Fatalf("Prepare on its own unused account = %v, %v; want a new temporary password", set, err)
	}
	if c := f.passwords["mine"]; c.Value != "N0vaSenha!x" || !c.Temporary {
		t.Fatalf("reset must set the new temporary password: %+v", c)
	}
	if f.users["mine"]["enabled"] != true {
		t.Fatal("an account disabled by a cancelled invitation comes back with the new invitation")
	}
	if f.users["mine"]["firstName"] != "Keep" || f.users["mine"]["attributes"] == nil {
		t.Fatal("re-enabling must write back the whole representation (names and attributes kept)")
	}

	set, err = k.Prepare(context.Background(), "jaentrou@empresa.com", "N0vaSenha!x")
	if err != nil || set {
		t.Fatalf("Prepare on an account already in use = %v, %v; want it untouched", set, err)
	}
	if _, touched := f.passwords["done"]; touched {
		t.Fatal("the password of an account in use must never be reset")
	}
}

func TestKeycloakLoginsDisablesOnlyItsOwnUnusedAccount(t *testing.T) {
	f, k := newFakeKeycloak(t)
	f.addUser("mine", "cancelada@empresa.com", true, true, true)
	f.addUser("done", "ativa@empresa.com", true, false, true)
	if err := k.Disable(context.Background(), "cancelada@empresa.com"); err != nil {
		t.Fatal(err)
	}
	if err := k.Disable(context.Background(), "ativa@empresa.com"); err != nil {
		t.Fatal(err)
	}
	if err := k.Disable(context.Background(), "ninguem@empresa.com"); err != nil {
		t.Fatal(err)
	}
	if f.users["mine"]["enabled"] != false || f.users["done"]["enabled"] != true {
		t.Fatalf("only the unused account OMNIRA created is disabled: mine=%v done=%v", f.users["mine"]["enabled"], f.users["done"]["enabled"])
	}
}

func TestKeycloakLoginsReusesTheTokenAndKeepsSecretsOutOfErrors(t *testing.T) {
	f, k := newFakeKeycloak(t)
	if _, err := k.Prepare(context.Background(), "um@empresa.com", "Temp0rary!x"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Prepare(context.Background(), "dois@empresa.com", "Temp0rary!x"); err != nil {
		t.Fatal(err)
	}
	if f.tokenCalls != 1 {
		t.Fatalf("token endpoint called %d times, want 1 (cached)", f.tokenCalls)
	}
	f.failAdmin = http.StatusInternalServerError
	_, err := k.Prepare(context.Background(), "tres@empresa.com", "Temp0rary!x")
	if err == nil {
		t.Fatal("a Keycloak failure must surface as an error")
	}
	for _, secret := range []string{fakeSecret, "tok", "Temp0rary!x", "tres@empresa.com"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q leaks %q", err, secret)
		}
	}

	bad := NewKeycloakLogins(strings.TrimSuffix(k.base, "/"), "omnira", "omnira-provisioner", "wrong")
	if _, err := bad.Prepare(context.Background(), "x@empresa.com", "Temp0rary!x"); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("a refused client must fail without echoing its secret: %v", err)
	}
}

func TestRealmFromIssuer(t *testing.T) {
	for issuer, want := range map[string]string{
		"https://auth.devops.k3gsolutions.com.br/realms/omnira": "omnira",
		"http://keycloak:8080/realms/omnira/":                   "omnira",
		"https://example.com/no-realm":                          "",
	} {
		if got := RealmFromIssuer(issuer); got != want {
			t.Errorf("RealmFromIssuer(%q) = %q, want %q", issuer, got, want)
		}
	}
}
