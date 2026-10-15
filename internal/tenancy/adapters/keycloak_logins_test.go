package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeKeycloak answers the few admin REST calls KeycloakLogins makes, like Keycloak 25 with unmanaged attributes editable by admins.
type fakeKeycloak struct {
	mu         sync.Mutex
	users      map[string]map[string]any
	passwords  map[string]kcCredential
	tokenCalls int
	failAdmin  int
	puts       []string
	deleted    []string
	// dropUnmanaged simulates a realm whose user profile drops undeclared attributes (no ADMIN_EDIT).
	dropUnmanaged bool
	// noLocation: POST /users answers without the new id.
	noLocation bool
	// onGetByID runs when GET /users/{id} is served: another actor changing the account between the search and the write.
	onGetByID func(id string)
}

const fakeSecret = "provisioner-s3cret"

func newFakeKeycloak(t *testing.T) (*fakeKeycloak, *KeycloakLogins) {
	t.Helper()
	f := &fakeKeycloak{users: map[string]map[string]any{}, passwords: map[string]kcCredential{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, NewKeycloakLogins(srv.URL+"/", "omnira", "omnira-provisioner", fakeSecret)
}

// loginFor builds the LoginInvitation of a test: valid for a week unless said otherwise.
func loginFor(email, id, password string, replaces ...string) LoginInvitation {
	return LoginInvitation{Email: email, Password: password, ID: id, ExpiresAt: time.Now().Add(7 * 24 * time.Hour), Replaces: replaces}
}

// bind marks a fake account as created for invitation id, valid until expires; disabledByOmnira as turned off by a cancellation.
func (f *fakeKeycloak) bind(userID, invitationID string, expires time.Time, disabledByOmnira bool) {
	attrs := f.users[userID]["attributes"].(map[string]any)
	attrs[invitationAttribute] = []any{invitationID}
	attrs[expiresAttribute] = []any{expires.UTC().Format(time.RFC3339)}
	if disabledByOmnira {
		attrs[disabledAttribute] = []any{"true"}
	}
}

func (f *fakeKeycloak) attr(userID, name string) string {
	if v, ok := f.users[userID]["attributes"].(map[string]any)[name].([]any); ok && len(v) > 0 {
		return v[0].(string)
	}
	return ""
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
			if !f.dropUnmanaged {
				attrs[k] = []any{v[0]}
			}
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
		if !f.noLocation {
			w.Header().Set("Location", "/admin/realms/omnira/users/"+id)
		}
		w.WriteHeader(http.StatusCreated)
	case strings.HasSuffix(path, "/reset-password") && r.Method == http.MethodPut:
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/users/"), "/reset-password")
		var c kcCredential
		_ = json.NewDecoder(r.Body).Decode(&c)
		f.passwords[id] = c
		f.puts = append(f.puts, "reset:"+id)
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(path, "/users/") && r.Method == http.MethodGet:
		if f.onGetByID != nil {
			f.onGetByID(strings.TrimPrefix(path, "/users/"))
		}
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
	case strings.HasPrefix(path, "/users/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(path, "/users/")
		delete(f.users, id)
		f.deleted = append(f.deleted, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestKeycloakLoginsCreatesAMissingAccountWithATemporaryPassword(t *testing.T) {
	f, k := newFakeKeycloak(t)
	inv := loginFor("nova@empresa.com", "inv-1", "Temp0rary!x")
	set, err := k.Prepare(context.Background(), inv)
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
	if f.attr("kc-nova@empresa.com", invitationAttribute) != "inv-1" || f.attr("kc-nova@empresa.com", expiresAttribute) != inv.ExpiresAt.UTC().Format(time.RFC3339) {
		t.Fatalf("created account must be bound to its invitation and its expiry: %+v", u["attributes"])
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
		set, err := k.Prepare(context.Background(), loginFor(email, "inv-1", "Temp0rary!x"))
		if err != nil || set {
			t.Fatalf("Prepare(%s) = %v, %v; want the account left alone", email, set, err)
		}
		if off, err := k.Disable(context.Background(), email, "inv-1"); err != nil || off {
			t.Fatalf("Disable(%s) = %v, %v; want nothing turned off", email, off, err)
		}
	}
	if len(f.puts) != 0 || len(f.passwords) != 0 {
		t.Fatalf("no write may reach an account OMNIRA did not create: puts=%v passwords=%v", f.puts, f.passwords)
	}
}

func TestKeycloakLoginsResetsAndReenablesOnlyItsOwnUnusedAccount(t *testing.T) {
	f, k := newFakeKeycloak(t)
	f.addUser("mine", "convidada@empresa.com", true, true, false)
	f.bind("mine", "inv-old", time.Now().Add(time.Hour), true) // its invitation was cancelled
	f.addUser("done", "jaentrou@empresa.com", true, false, true)

	set, err := k.Prepare(context.Background(), loginFor("convidada@empresa.com", "inv-new", "N0vaSenha!x"))
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
	if f.attr("mine", invitationAttribute) != "inv-new" || f.attr("mine", disabledAttribute) != "" {
		t.Fatalf("the account now waits for the new invitation and is no longer marked as turned off: %+v", f.users["mine"]["attributes"])
	}

	set, err = k.Prepare(context.Background(), loginFor("jaentrou@empresa.com", "inv-new", "N0vaSenha!x"))
	if err != nil || set {
		t.Fatalf("Prepare on an account already in use = %v, %v; want it untouched", set, err)
	}
	if _, touched := f.passwords["done"]; touched {
		t.Fatal("the password of an account in use must never be reset")
	}
}

func TestKeycloakLoginsDisablesOnlyTheUnusedAccountOfThatInvitation(t *testing.T) {
	f, k := newFakeKeycloak(t)
	week := time.Now().Add(7 * 24 * time.Hour)
	f.addUser("mine", "cancelada@empresa.com", true, true, true)
	f.bind("mine", "inv-a", week, false)
	f.addUser("done", "ativa@empresa.com", true, false, true)
	f.bind("done", "inv-a2", week, false)
	f.addUser("other", "outra@empresa.com", true, true, true)
	f.bind("other", "inv-b", week, false) // waits for an invitation of another instance

	if off, err := k.Disable(context.Background(), "outra@empresa.com", "inv-a"); err != nil || off {
		t.Fatalf("cancelling invitation A must not turn off the account invitation B waits on: %v, %v", off, err)
	}
	if off, err := k.Disable(context.Background(), "cancelada@empresa.com", "inv-a"); err != nil || !off {
		t.Fatalf("Disable of its own unused account = %v, %v; want it turned off", off, err)
	}
	if off, err := k.Disable(context.Background(), "ativa@empresa.com", "inv-a2"); err != nil || off {
		t.Fatalf("an account in use is never turned off: %v, %v", off, err)
	}
	if off, err := k.Disable(context.Background(), "ninguem@empresa.com", "inv-a"); err != nil || off {
		t.Fatalf("no account, nothing to turn off: %v, %v", off, err)
	}
	if f.users["mine"]["enabled"] != false || f.attr("mine", disabledAttribute) != "true" {
		t.Fatalf("the cancelled invitation's account is off and marked as turned off by OMNIRA: %+v", f.users["mine"])
	}
	if f.users["done"]["enabled"] != true || f.users["other"]["enabled"] != true {
		t.Fatal("only the unused account of the cancelled invitation is turned off")
	}
}

func TestKeycloakLoginsLeavesAnAccountWaitingOnAnotherLiveInvitation(t *testing.T) {
	f, k := newFakeKeycloak(t)
	f.addUser("b", "disputada@empresa.com", true, true, true)
	f.bind("b", "inv-b", time.Now().Add(7*24*time.Hour), false)
	f.passwords["b"] = kcCredential{Type: "password", Value: "Senha-De-B1", Temporary: true}

	set, err := k.Prepare(context.Background(), loginFor("disputada@empresa.com", "inv-a", "Senha-De-A1"))
	if err != nil || set {
		t.Fatalf("Prepare for invitation A = %v, %v; want the account of live invitation B left alone", set, err)
	}
	if f.passwords["b"].Value != "Senha-De-B1" || f.attr("b", invitationAttribute) != "inv-b" || len(f.puts) != 0 {
		t.Fatalf("invitation A must not reset, rebind or rewrite B's account: password=%v puts=%v", f.passwords["b"], f.puts)
	}
}

func TestKeycloakLoginsTakesOverWhenTheBoundInvitationIsReplacedOrOver(t *testing.T) {
	f, k := newFakeKeycloak(t)
	week := time.Now().Add(7 * 24 * time.Hour)
	f.addUser("replaced", "reconvite@empresa.com", true, true, true)
	f.bind("replaced", "inv-1", week, false)
	f.addUser("expired", "vencido@empresa.com", true, true, true)
	f.bind("expired", "inv-x", time.Now().Add(-time.Minute), false)

	// the same instance invites the e-mail again: the new invitation replaces inv-1 and takes its account
	if set, err := k.Prepare(context.Background(), loginFor("reconvite@empresa.com", "inv-2", "N0va-Senha1", "inv-1")); err != nil || !set {
		t.Fatalf("re-invitation = %v, %v; want the replaced invitation's account taken over", set, err)
	}
	// an invitation of another instance, after the bound one expired
	if set, err := k.Prepare(context.Background(), loginFor("vencido@empresa.com", "inv-y", "N0va-Senha2")); err != nil || !set {
		t.Fatalf("after the bound invitation expired = %v, %v; want the account taken over", set, err)
	}
	if f.passwords["replaced"].Value != "N0va-Senha1" || f.attr("replaced", invitationAttribute) != "inv-2" {
		t.Fatalf("replaced: %v %v", f.passwords["replaced"], f.users["replaced"]["attributes"])
	}
	if f.passwords["expired"].Value != "N0va-Senha2" || f.attr("expired", invitationAttribute) != "inv-y" {
		t.Fatalf("expired: %v %v", f.passwords["expired"], f.users["expired"]["attributes"])
	}
}

func TestKeycloakLoginsNeverTurnsBackOnAnAccountAnAdministratorDisabled(t *testing.T) {
	f, k := newFakeKeycloak(t)
	f.addUser("blocked", "bloqueada@empresa.com", true, true, false)
	f.bind("blocked", "inv-1", time.Now().Add(time.Hour), false) // off, but not by OMNIRA

	set, err := k.Prepare(context.Background(), loginFor("bloqueada@empresa.com", "inv-1", "N0va-Senha1"))
	if err == nil || set {
		t.Fatalf("Prepare = %v, %v; want a refusal for an account an administrator turned off", set, err)
	}
	if f.users["blocked"]["enabled"] != false || len(f.puts) != 0 {
		t.Fatalf("the account must stay as the administrator left it: %+v puts=%v", f.users["blocked"], f.puts)
	}
}

func TestKeycloakLoginsFailsClosedWhenTheRealmDropsItsAttributes(t *testing.T) {
	f, k := newFakeKeycloak(t)
	f.dropUnmanaged = true
	set, err := k.Prepare(context.Background(), loginFor("semmarca@empresa.com", "inv-1", "Temp0rary!x"))
	if err == nil || set {
		t.Fatalf("Prepare = %v, %v; want an error when the binding cannot be kept", set, err)
	}
	if _, left := f.users["kc-semmarca@empresa.com"]; left || len(f.deleted) != 1 {
		t.Fatalf("the account created without its binding must be removed: users=%v deleted=%v", f.users, f.deleted)
	}
	if strings.Contains(err.Error(), "Temp0rary!x") {
		t.Fatalf("error leaks the password: %v", err)
	}
}

func TestKeycloakLoginsFailsClosedOnAnIncompleteBinding(t *testing.T) {
	f, k := newFakeKeycloak(t)
	f.addUser("legacy", "semvinculo@empresa.com", true, true, true) // OMNIRA's marker, no invitation
	f.addUser("broken", "validade@empresa.com", true, true, true)
	f.bind("broken", "inv-b", time.Now().Add(-time.Hour), false)
	f.users["broken"]["attributes"].(map[string]any)[expiresAttribute] = []any{"not-a-date"}
	for _, email := range []string{"semvinculo@empresa.com", "validade@empresa.com"} {
		if set, err := k.Prepare(context.Background(), loginFor(email, "inv-a", "N0va-Senha1")); err != nil || set {
			t.Fatalf("Prepare(%s) = %v, %v; an account without a complete binding is nobody's to take", email, set, err)
		}
	}
	if len(f.puts) != 0 || len(f.passwords) != 0 {
		t.Fatalf("nothing may be written to an account with an incomplete binding: puts=%v passwords=%v", f.puts, f.passwords)
	}
}

func TestKeycloakLoginsChecksTheAccountAgainRightBeforeWriting(t *testing.T) {
	f, k := newFakeKeycloak(t)
	f.addUser("x", "corrida@empresa.com", true, true, true)
	// expired: inv-b may take it, but another invitation takes it first, between the search and the write
	f.bind("x", "inv-a", time.Now().Add(-time.Minute), false)
	f.onGetByID = func(id string) { f.bind(id, "inv-c", time.Now().Add(7*24*time.Hour), false) }
	if set, err := k.Prepare(context.Background(), loginFor("corrida@empresa.com", "inv-b", "N0va-Senha1")); err == nil || set {
		t.Fatalf("Prepare = %v, %v; want a refusal when the account changed after the search", set, err)
	}
	f.onGetByID = nil
	f.bind("x", "inv-a", time.Now().Add(time.Hour), false)
	f.onGetByID = func(id string) { f.bind(id, "inv-c", time.Now().Add(time.Hour), false) }
	if off, err := k.Disable(context.Background(), "corrida@empresa.com", "inv-a"); err == nil || off {
		t.Fatalf("Disable = %v, %v; want a refusal when the account is no longer this invitation's", off, err)
	}
	if len(f.puts) != 0 || f.users["x"]["enabled"] != true {
		t.Fatalf("nothing may be written after the account changed: puts=%v", f.puts)
	}
}

func TestKeycloakLoginsRemovesOnlyTheAccountItCreated(t *testing.T) {
	f, k := newFakeKeycloak(t)
	f.dropUnmanaged, f.noLocation = true, true
	if set, err := k.Prepare(context.Background(), loginFor("semid@empresa.com", "inv-1", "Temp0rary!x")); err == nil || set {
		t.Fatalf("Prepare = %v, %v; want an error when the new account's id is unknown", set, err)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("without the id Keycloak answered, nothing is removed: %v", f.deleted)
	}
}

func TestKeycloakLoginsReusesTheTokenAndKeepsSecretsOutOfErrors(t *testing.T) {
	f, k := newFakeKeycloak(t)
	if _, err := k.Prepare(context.Background(), loginFor("um@empresa.com", "inv-1", "Temp0rary!x")); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Prepare(context.Background(), loginFor("dois@empresa.com", "inv-2", "Temp0rary!x")); err != nil {
		t.Fatal(err)
	}
	if f.tokenCalls != 1 {
		t.Fatalf("token endpoint called %d times, want 1 (cached)", f.tokenCalls)
	}
	f.failAdmin = http.StatusInternalServerError
	_, err := k.Prepare(context.Background(), loginFor("tres@empresa.com", "inv-3", "Temp0rary!x"))
	if err == nil {
		t.Fatal("a Keycloak failure must surface as an error")
	}
	for _, secret := range []string{fakeSecret, "tok", "Temp0rary!x", "tres@empresa.com"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q leaks %q", err, secret)
		}
	}

	bad := NewKeycloakLogins(strings.TrimSuffix(k.base, "/"), "omnira", "omnira-provisioner", "wrong")
	if _, err := bad.Prepare(context.Background(), loginFor("x@empresa.com", "inv-4", "Temp0rary!x")); err == nil || strings.Contains(err.Error(), "wrong") {
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
