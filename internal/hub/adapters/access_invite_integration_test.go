package adapters_test

// Authorizing a person BY E-MAIL (ADR-0039 §3.10) on a real PostgreSQL: an existing account becomes an agent at once with
// exactly the access that was picked; an e-mail without an account is kept and takes effect at the first sign-in whose
// address the identity provider VERIFIED, only while the administrator who wrote it is still an admin, never after it
// expired or was cancelled, and exactly once even when two sign-ins race.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/access"
	"github.com/omnira/omnira/internal/platform/authn"
)

type signIn struct {
	w        *world
	resolver *authn.PostgresIdentityResolver
}

// newSignIn is the real sign-in provisioning with the same hook the server wires.
func newSignIn(w *world, svc *access.Service) *signIn {
	r := authn.NewPostgresIdentityResolver(w.app).WithAfterProvision(func(ctx context.Context, user uuid.UUID) {
		if _, err := svc.ApplyPreauthorizations(ctx, user); err != nil {
			w.t.Errorf("applying authorizations at sign-in: %v", err)
		}
	})
	return &signIn{w: w, resolver: r}
}

func (s *signIn) login(subject, email string, verified bool) uuid.UUID {
	s.w.t.Helper()
	id, err := s.resolver.ProvisionIdentity(s.w.ctx, "https://idp.test/realm", subject+"-"+s.w.hub.String()[:8], email, "Pessoa Nova", verified)
	s.w.must(err)
	return id
}

func (a *accessAPI) invite(admin uuid.UUID, email string, access ...map[string]string) (int, string) {
	if access == nil {
		access = []map[string]string{}
	}
	return a.call("POST", a.base()+"/invitations", admin, map[string]any{"email": email, "access": access})
}

func (a *accessAPI) tenantAccess(key, mode string) map[string]string {
	return map[string]string{"tenant_id": a.w.tenant[key].String(), "mode": mode}
}

// e makes an address that cannot collide with the other tests, which share one database (two active accounts with the
// same address read as "no single account", on purpose).
func (w *world) e(name string) string { return name + "-" + w.hub.String()[:8] + "@example.com" }

func (w *world) pending(email string) int {
	return w.count(`SELECT count(*) FROM hub_preauthorizations WHERE hub_id = $1 AND email = $2 AND status = 'pending'`, w.hub, email)
}

func (w *world) preStatus(email string) string {
	var st string
	w.must(w.owner.QueryRow(w.ctx, `SELECT status FROM hub_preauthorizations WHERE hub_id = $1 AND email = $2 ORDER BY created_at DESC LIMIT 1`, w.hub, email).Scan(&st))
	return st
}

func TestAccessInvite_AnExistingAccountBecomesAnAgentNowWithExactlyThePickedAccess(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	person := w.user("person")
	w.email(person, w.e("pessoa"))

	code, body := api.invite(admin, " "+strings.ToUpper(w.e("pessoa"))+" ", api.tenantAccess("A", "reply"), api.tenantAccess("B", "read"))
	var res access.InviteResult
	w.must(json.Unmarshal([]byte(body), &res))
	if code != 200 || res.Status != "applied" {
		t.Fatalf("existing account: %d %s", code, body)
	}
	if w.count(`SELECT count(*) FROM hub_memberships hm JOIN roles r ON r.id = hm.role_id WHERE hm.hub_id = $1 AND hm.user_id = $2 AND r.key = 'hub_agent'`, w.hub, person) != 1 {
		t.Fatal("the person did not become a hub agent")
	}
	if w.count(`SELECT count(*) FROM hub_preauthorizations WHERE hub_id = $1`, w.hub) != 0 {
		t.Fatal("an existing account must not leave a waiting authorization behind")
	}
	// the database itself now lets the person read A and B, and nothing of C
	if w.reads(person, "A").conversations == 0 || w.reads(person, "B").conversations == 0 || w.reads(person, "C").conversations != 0 {
		t.Fatalf("the picked access is not what the database enforces: A=%+v B=%+v C=%+v", w.reads(person, "A"), w.reads(person, "B"), w.reads(person, "C"))
	}
	if w.count(`SELECT count(*) FROM effective_access_grants WHERE user_id = $1 AND tenant_id = $2 AND can_reply AND status = 'active'`, person, w.tenant["A"]) != 1 ||
		w.count(`SELECT count(*) FROM effective_access_grants WHERE user_id = $1 AND tenant_id = $2 AND NOT can_reply AND status = 'active'`, person, w.tenant["B"]) != 1 {
		t.Fatal("reply on A and read on B expected")
	}
	if w.auditActor("hub.member.added", admin) != 1 {
		t.Fatal("the authorization is not attributed to the administrator")
	}
	// the same call again changes nothing and still works
	if code, _ := api.invite(admin, w.e("pessoa"), api.tenantAccess("A", "reply")); code != 200 {
		t.Fatalf("repeating: %d", code)
	}
	if w.count(`SELECT count(*) FROM effective_access_grants WHERE user_id = $1`, person) != 2 {
		t.Fatal("repeating duplicated grants")
	}
}

func TestAccessInvite_AnEmailWithoutAnAccountIsKeptListedAndReplacedByAskingAgain(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")

	before := time.Now()
	code, body := api.invite(admin, strings.ToUpper(w.e("futuro")), api.tenantAccess("A", "reply"))
	var res access.InviteResult
	w.must(json.Unmarshal([]byte(body), &res))
	if code != 200 || res.Status != "pending" || res.ExpiresAt == nil {
		t.Fatalf("unknown account: %d %s", code, body)
	}
	if d := res.ExpiresAt.Sub(before); d < 13*24*time.Hour || d > 15*24*time.Hour {
		t.Fatalf("an authorization waits about 14 days, not %v", d)
	}
	if w.count(`SELECT count(*) FROM hub_memberships WHERE hub_id = $1`, w.hub) != 1 { // only the admin
		t.Fatal("waiting for a sign-in must not create a membership")
	}
	if code, ov := api.overview(admin); code != 200 || len(ov.Invitations) != 1 || ov.Invitations[0].Email != w.e("futuro") || len(ov.Invitations[0].Access) != 1 {
		t.Fatalf("overview: %d %+v", code, ov.Invitations)
	}
	// asking again for the same address replaces it, it does not stack
	if code, _ := api.invite(admin, w.e("futuro"), api.tenantAccess("B", "read")); code != 200 {
		t.Fatal("second ask refused")
	}
	if w.pending(w.e("futuro")) != 1 {
		t.Fatalf("pending: %d, want exactly one", w.pending(w.e("futuro")))
	}
	if _, ov := api.overview(admin); len(ov.Invitations) != 1 || ov.Invitations[0].Access[0].TenantID != w.tenant["B"] {
		t.Fatalf("the replacement is not what is listed: %+v", ov.Invitations)
	}
	if w.auditActor("hub.preauthorization.created", admin) != 2 {
		t.Fatal("creations are not on the audit trail")
	}
}

func TestAccessInvite_TakesEffectAtTheFirstVerifiedSignInAndOnlyOnce(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	in := newSignIn(w, api.svc)
	if code, _ := api.invite(admin, w.e("novata"), api.tenantAccess("A", "reply"), api.tenantAccess("C", "read")); code != 200 {
		t.Fatal("invite refused")
	}

	user := in.login("sub-novata", w.e("novata"), true)
	if w.count(`SELECT count(*) FROM hub_memberships hm JOIN roles r ON r.id = hm.role_id WHERE hm.hub_id = $1 AND hm.user_id = $2 AND r.key = 'hub_agent'`, w.hub, user) != 1 {
		t.Fatal("the first verified sign-in did not make the person a hub agent")
	}
	if w.reads(user, "A").conversations == 0 || w.reads(user, "C").conversations == 0 || w.reads(user, "B").conversations != 0 {
		t.Fatalf("what the database lets the new person read is not what was picked: A=%+v B=%+v C=%+v", w.reads(user, "A"), w.reads(user, "B"), w.reads(user, "C"))
	}
	if w.preStatus(w.e("novata")) != "applied" || w.count(`SELECT count(*) FROM hub_preauthorizations WHERE applied_user_id = $1`, user) != 1 {
		t.Fatal("the authorization was not marked applied to this person")
	}
	if w.auditActor("hub.preauthorization.applied", user) != 1 || w.auditActor("hub.member.added", admin) != 1 {
		t.Fatal("application is not on the audit trail (the person applied it, the administrator's authority is the actor of the writes)")
	}
	if _, ov := api.overview(admin); len(ov.Invitations) != 0 {
		t.Fatalf("an applied authorization is still listed as waiting: %+v", ov.Invitations)
	}

	// the administrator takes the access back; the next sign-in must NOT bring it back
	if code := api.set(admin, user, "A", "none", nil); code != 204 {
		t.Fatalf("revoking: %d", code)
	}
	in.login("sub-novata", w.e("novata"), true)
	if w.reads(user, "A").conversations != 0 {
		t.Fatal("a later sign-in resurrected an authorization that had already been applied")
	}
	if w.auditActor("hub.preauthorization.applied", user) != 1 {
		t.Fatal("applied twice")
	}
}

func TestAccessInvite_NeverTakesEffectWithoutAVerifiedAddress(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	in := newSignIn(w, api.svc)
	if code, _ := api.invite(admin, w.e("naoverificada"), api.tenantAccess("A", "read")); code != 200 {
		t.Fatal("invite refused")
	}
	// someone registers the address at the identity provider without proving they own it
	user := in.login("sub-nv", w.e("naoverificada"), false)
	if w.count(`SELECT count(*) FROM hub_memberships WHERE user_id = $1`, user) != 0 || w.reads(user, "A").conversations != 0 {
		t.Fatal("an unverified address received the authorization")
	}
	if w.pending(w.e("naoverificada")) != 1 {
		t.Fatal("an unverified sign-in must leave the authorization waiting")
	}
	// the same person later verifies it
	in.login("sub-nv", w.e("naoverificada"), true)
	if w.reads(user, "A").conversations == 0 || w.pending(w.e("naoverificada")) != 0 {
		t.Fatal("after verification the authorization must take effect")
	}
}

func TestAccessInvite_ExpiredCancelledAndOrphanedAuthorizationsNeverTakeEffect(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	leaver := w.hubAdmin("leaver")
	in := newSignIn(w, api.svc)

	// expired
	api.invite(admin, w.e("vencida"), api.tenantAccess("A", "read"))
	w.exec(`UPDATE hub_preauthorizations SET expires_at = now() - interval '1 minute' WHERE email = $1`, w.e("vencida"))
	u1 := in.login("sub-venc", w.e("vencida"), true)
	if w.reads(u1, "A").conversations != 0 || w.count(`SELECT count(*) FROM hub_memberships WHERE user_id = $1`, u1) != 0 {
		t.Fatal("an expired authorization took effect")
	}
	if _, ov := api.overview(admin); len(ov.Invitations) != 0 {
		t.Fatalf("an expired authorization is still offered: %+v", ov.Invitations)
	}

	// cancelled by the panel
	api.invite(admin, w.e("cancelada"), api.tenantAccess("A", "read"))
	_, ov := api.overview(admin)
	if len(ov.Invitations) != 1 {
		t.Fatalf("setup: %+v", ov.Invitations)
	}
	id := ov.Invitations[0].ID.String()
	if code, _ := api.call("DELETE", api.base()+"/invitations/"+id, admin, nil); code != 204 {
		t.Fatal("cancel refused")
	}
	if code, _ := api.call("DELETE", api.base()+"/invitations/"+id, admin, nil); code != 404 {
		t.Fatalf("cancelling twice: %d, want 404", code)
	}
	u2 := in.login("sub-canc", w.e("cancelada"), true)
	if w.reads(u2, "A").conversations != 0 {
		t.Fatal("a cancelled authorization took effect")
	}

	// the administrator who wrote it stopped being one before the person ever signed in
	api.invite(leaver, w.e("orfa"), api.tenantAccess("A", "reply"))
	w.exec(`UPDATE hub_memberships SET role_id = $3 WHERE hub_id = $1 AND user_id = $2`, w.hub, leaver, w.roleHubAgent)
	u3 := in.login("sub-orfa", w.e("orfa"), true)
	if w.reads(u3, "A").conversations != 0 || w.count(`SELECT count(*) FROM hub_memberships WHERE user_id = $1`, u3) != 0 {
		t.Fatal("an authorization outlived the administrator who wrote it")
	}
	if w.preStatus(w.e("orfa")) != "void" {
		t.Fatalf("status %q, want void", w.preStatus(w.e("orfa")))
	}
}

func TestAccessInvite_ACompanySuspendedMeanwhileIsSkippedTheRestStillApplies(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	in := newSignIn(w, api.svc)
	api.invite(admin, w.e("parcial"), api.tenantAccess("A", "reply"), api.tenantAccess("B", "read"))
	w.exec(`UPDATE tenants SET status = 'suspended' WHERE id = $1`, w.tenant["B"])
	user := in.login("sub-parcial", w.e("parcial"), true)
	if w.reads(user, "A").conversations == 0 {
		t.Fatal("the healthy company was not applied")
	}
	if w.count(`SELECT count(*) FROM effective_access_grants WHERE user_id = $1 AND tenant_id = $2`, user, w.tenant["B"]) != 0 {
		t.Fatal("a suspended company received a grant")
	}
	if w.count(`SELECT count(*) FROM audit_events WHERE action = 'hub.preauthorization.applied' AND actor_id = $1 AND metadata ->> 'skipped' = '1'`, user) != 1 {
		t.Fatal("the skipped company is not recorded")
	}
}

func TestAccessInvite_TwoSimultaneousSignInsApplyItExactlyOnce(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	api.invite(admin, w.e("corrida"), api.tenantAccess("A", "reply"))
	user := w.user("corrida")
	w.email(user, w.e("corrida"))
	w.exec(`INSERT INTO user_identities (user_id, issuer, subject, email, email_verified) VALUES ($1, 'https://idp.test/realm', $2, $3, true)`, user, "sub-corrida-"+w.hub.String()[:8], w.e("corrida"))
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := api.svc.ApplyPreauthorizations(w.ctx, user); err != nil {
				t.Errorf("apply: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := w.auditActor("hub.preauthorization.applied", user); n != 1 {
		t.Fatalf("applied %d times, want exactly once", n)
	}
	if w.count(`SELECT count(*) FROM effective_access_grants WHERE user_id = $1`, user) != 1 || w.count(`SELECT count(*) FROM hub_memberships WHERE user_id = $1`, user) != 1 {
		t.Fatal("racing sign-ins duplicated rows")
	}
}

func TestAccessInvite_RequestValidationAndWhoMayAsk(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	agent := w.hubAgent("agent")
	tenantAdmin := w.user("tadm")
	w.exec(`INSERT INTO memberships (tenant_id, user_id, role_id) VALUES ($1, $2, $3)`, w.tenant["A"], tenantAdmin, w.role("tenant_admin"))

	for name, u := range map[string]uuid.UUID{"hub agent": agent, "company admin": tenantAdmin, "anonymous": uuid.Nil} {
		want := http.StatusNotFound
		if u == uuid.Nil {
			want = http.StatusUnauthorized
		}
		if code, _ := api.invite(u, w.e("x"), api.tenantAccess("A", "read")); code != want {
			t.Errorf("%s inviting: %d, want %d", name, code, want)
		}
		if code, _ := api.call("DELETE", api.base()+"/invitations/"+uuid.NewString(), u, nil); code != want {
			t.Errorf("%s cancelling: %d, want %d", name, code, want)
		}
	}
	if w.count(`SELECT count(*) FROM hub_preauthorizations WHERE hub_id = $1`, w.hub) != 0 {
		t.Fatal("a refused caller wrote an authorization")
	}

	for name, tc := range map[string]struct {
		email  string
		access []map[string]string
		want   int
	}{
		"no @":               {"semarroba", nil, 422},
		"two @":              {"a@b@c", nil, 422},
		"spaces inside":      {"a b@c.com", nil, 422},
		"empty":              {"  ", nil, 422},
		"mode none":          {w.e("a"), []map[string]string{api.tenantAccess("A", "none")}, 422},
		"unknown mode":       {w.e("a"), []map[string]string{api.tenantAccess("A", "admin")}, 422},
		"same company twice": {w.e("a"), []map[string]string{api.tenantAccess("A", "read"), api.tenantAccess("A", "reply")}, 422},
		"company of nobody":  {w.e("a"), []map[string]string{{"tenant_id": uuid.NewString(), "mode": "read"}}, 404},
	} {
		if code, body := api.invite(admin, tc.email, tc.access...); code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, code, body, tc.want)
		}
	}
	// too many companies
	many := make([]map[string]string, 51)
	for i := range many {
		many[i] = map[string]string{"tenant_id": uuid.NewString(), "mode": "read"}
	}
	if code, _ := api.invite(admin, w.e("a"), many...); code != 422 {
		t.Errorf("51 companies: %d", code)
	}
	// a company of ANOTHER hub is not this hub's to hand out
	otherHub, foreign := uuid.New(), uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'Other')`, otherHub)
	w.exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, 'Foreign', 'active')`, foreign)
	w.exec(`INSERT INTO hub_tenant_service_contracts (hub_id, tenant_id, valid_from) VALUES ($1, $2, now() - interval '1 day')`, otherHub, foreign)
	if code, _ := api.invite(admin, w.e("a"), map[string]string{"tenant_id": foreign.String(), "mode": "read"}); code != 404 {
		t.Errorf("a company of another hub: %d, want 404", code)
	}
	// a body with an unknown field or a second JSON value is refused
	if code, _ := api.call("POST", api.base()+"/invitations", admin, map[string]any{"email": w.e("a"), "access": []any{}, "role": "hub_admin"}); code != 400 {
		t.Errorf("unknown field: %d", code)
	}
	if w.count(`SELECT count(*) FROM hub_preauthorizations WHERE hub_id = $1`, w.hub) != 0 {
		t.Fatal("a refused request wrote an authorization")
	}
	// a hub_admin target cannot be demoted by inviting them as an agent
	other := w.hubAdmin("other")
	w.email(other, w.e("chefe"))
	if code, _ := api.invite(admin, w.e("chefe"), api.tenantAccess("A", "read")); code != 200 {
		t.Fatalf("inviting an existing admin to a company: %d", code)
	}
	if w.count(`SELECT count(*) FROM hub_memberships hm JOIN roles r ON r.id = hm.role_id WHERE hm.user_id = $1 AND r.key = 'hub_admin'`, other) != 1 {
		t.Fatal("inviting a hub admin demoted them")
	}
}

func TestAccessInvite_AnInactiveAccountIsTreatedLikeNoAccountAndAnotherHubCannotCancelIt(t *testing.T) {
	w := newWorld(t)
	api := newAccessAPI(t, w)
	admin := w.hubAdmin("admin")
	gone := w.user("gone")
	w.email(gone, w.e("inativa"))
	w.exec(`UPDATE users SET status = 'inactive' WHERE id = $1`, gone)

	code, body := api.invite(admin, w.e("inativa"), api.tenantAccess("A", "read"))
	var res access.InviteResult
	w.must(json.Unmarshal([]byte(body), &res))
	if code != 200 || res.Status != "pending" || w.count(`SELECT count(*) FROM hub_memberships WHERE user_id = $1`, gone) != 0 {
		t.Fatalf("an inactive account must not be made an agent: %d %s", code, body)
	}

	// the administrator of ANOTHER hub cannot see or cancel this hub's authorizations
	otherHub := uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'Other')`, otherHub)
	otherAdmin := w.user("otheradmin")
	w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, otherHub, otherAdmin, w.roleHubAdmin)
	var id uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM hub_preauthorizations WHERE hub_id = $1 AND email = $2`, w.hub, w.e("inativa")).Scan(&id))
	if code, _ := api.call("DELETE", "/api/v1/hubs/"+otherHub.String()+"/access/invitations/"+id.String(), otherAdmin, nil); code != 404 {
		t.Fatalf("cancelling another hub's authorization through your own hub: %d", code)
	}
	if code, _ := api.call("DELETE", api.base()+"/invitations/"+id.String(), otherAdmin, nil); code != 404 {
		t.Fatalf("cancelling it through the right hub as a stranger: %d", code)
	}
	if w.pending(w.e("inativa")) != 1 {
		t.Fatal("a stranger cancelled the authorization")
	}
}
