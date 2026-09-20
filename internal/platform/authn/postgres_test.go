package authn

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresIdentityResolverCrossesRLSOnlyForProvisionedIdentity(t *testing.T) {
	ownerURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if ownerURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}
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
