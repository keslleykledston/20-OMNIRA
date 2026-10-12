package authn

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
)

func TestPostgresIdentityResolverCrossesRLSOnlyForProvisionedIdentity(t *testing.T) {
	ownerURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil { t.Fatal(err) }
	defer owner.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil { t.Fatal(err) }
	defer app.Close()

	userID, tenantID := uuid.New(), uuid.New()
	subject := "oidc-test-" + uuid.NewString()
	issuer := "https://idp.test/realms/" + uuid.NewString()
	if _, err := owner.Exec(ctx, `INSERT INTO users(id,external_subject,email,display_name,status) VALUES($1,$2,$3,'OIDC Test','active')`, userID, issuer+"|"+subject, subject+"@invalid"); err != nil { t.Fatal(err) }
	// Authentication resolves through user_identities, so the identity row is
	// what makes this user reachable — not users.external_subject.
	if _, err := owner.Exec(ctx, `INSERT INTO user_identities(user_id,issuer,subject) VALUES($1,$2,$3)`, userID, issuer, subject); err != nil { t.Fatal(err) }
	if _, err := owner.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,'OIDC Tenant','active')`, tenantID); err != nil { t.Fatal(err) }
	if _, err := owner.Exec(ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) SELECT $1,$2,id,'active' FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`, tenantID, userID); err != nil { t.Fatal(err) }
	t.Cleanup(func() {
		_, _ = owner.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, tenantID)
		_, _ = owner.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})

	resolver := NewPostgresIdentityResolver(app)
	resolved, err := resolver.ResolveUserID(ctx, issuer, subject)
	if err != nil || resolved != userID {
		t.Fatalf("resolved=%s err=%v", resolved, err)
	}
	// Same subject from another issuer is a different identity and must not
	// resolve to this user.
	if _, err := resolver.ResolveUserID(ctx, "https://other-idp.test/realms/x", subject); err == nil {
		t.Fatal("subject from a different issuer was accepted")
	}
	profile, err := resolver.SessionProfile(ctx, userID)
	if err != nil || profile.User.ID != userID.String() || profile.Tenant.ID != tenantID.String() {
		t.Fatalf("profile=%#v err=%v", profile, err)
	}
	if _, err := resolver.ResolveUserID(ctx, issuer, "unprovisioned-"+uuid.NewString()); err == nil {
		t.Fatal("unprovisioned identity was accepted")
	}
}

// A person who belongs to no company but to a live Service Hub can finish signing in (their place is "Conversas"); anyone else still cannot, and a suspended Hub
// gives nobody a place.
func TestSessionProfileOfAPersonWhoOnlyBelongsToAHub(t *testing.T) {
	ownerURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	hubUser, nobody, hub := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO users(id,external_subject,email,display_name,status) VALUES($1,$2,'hubonly@invalid','Hub Only','active'),($3,$4,'nobody@invalid','Nobody','active')`,
		hubUser, "hubonly-"+hubUser.String(), nobody, "nobody-"+nobody.String())
	exec(`INSERT INTO service_hubs(id,name,status) VALUES($1,'Hub do teste','active')`, hub)
	exec(`INSERT INTO hub_memberships(hub_id,user_id,role_id) SELECT $1,$2,id FROM roles WHERE key='hub_agent' AND tenant_id IS NULL`, hub, hubUser)
	t.Cleanup(func() {
		_, _ = owner.Exec(context.Background(), `DELETE FROM service_hubs WHERE id=$1`, hub)
		_, _ = owner.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2)`, hubUser, nobody)
	})
	resolver := NewPostgresIdentityResolver(app)

	p, err := resolver.SessionProfile(ctx, hubUser)
	if err != nil || !p.HubOnly || p.Tenant.ID != "" || p.User.ID != hubUser.String() || p.User.Email != "hubonly@invalid" {
		t.Fatalf("hub-only profile = %#v err=%v", p, err)
	}
	if _, err := resolver.SessionProfile(ctx, nobody); err == nil {
		t.Fatal("a person with neither a company nor a Hub got a profile")
	}
	exec(`UPDATE service_hubs SET status='suspended' WHERE id=$1`, hub)
	if _, err := resolver.SessionProfile(ctx, hubUser); err == nil {
		t.Fatal("a member of a suspended Hub got a profile")
	}
	exec(`UPDATE service_hubs SET status='active' WHERE id=$1`, hub)
	exec(`UPDATE users SET status='inactive' WHERE id=$1`, hubUser)
	if _, err := resolver.SessionProfile(ctx, hubUser); err == nil {
		t.Fatal("an inactive account got a profile")
	}
}
