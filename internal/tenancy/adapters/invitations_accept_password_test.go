package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/password"
)

// The accept page (SSO) sends no password: identity is the session with the e-mail the IdP verified. A password, when
// sent, must match the invitation's temporary one and becomes the account's first password.

func acceptBody(t *testing.T, pw string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{"password": pw})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAcceptWithoutPasswordLeavesTheCredentialAlone(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	invitee := seedUnaffiliatedUser(t, seed, "sso@empresa.com")
	h := newDevInvitationsHandler(app)
	inv := createInvitation(t, app, h, f, admin, "sso@empresa.com", "tenant_agent")
	token := rawTokenFromURL(t, inv.InviteURL)

	rec, req := asPrincipal(invitee, http.MethodPost, "/x", map[string]string{"token": token}, []byte(`{}`))
	h.AcceptInvitation(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept without password = %d %s", rec.Code, rec.Body.String())
	}
	var hash *string
	var exp *string
	if err := seed.QueryRow(context.Background(),
		`SELECT password_hash, password_expires_at::text FROM users WHERE id=$1`, invitee).Scan(&hash, &exp); err != nil {
		t.Fatal(err)
	}
	if hash != nil || exp != nil {
		t.Fatalf("an SSO accept must not set a credential (hash=%v expires=%v)", hash, exp)
	}
}

func TestAcceptWithAnEmptyBodyIsAccepted(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	invitee := seedUnaffiliatedUser(t, seed, "empty@empresa.com")
	h := newDevInvitationsHandler(app)
	inv := createInvitation(t, app, h, f, admin, "empty@empresa.com", "tenant_agent")
	rec, req := asPrincipal(invitee, http.MethodPost, "/x", map[string]string{"token": rawTokenFromURL(t, inv.InviteURL)}, nil)
	h.AcceptInvitation(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept with no body = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAcceptWithTheTemporaryPasswordSetsTheCredential(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	invitee := seedUnaffiliatedUser(t, seed, "pw@empresa.com")
	h := newDevInvitationsHandler(app)
	inv := createInvitation(t, app, h, f, admin, "pw@empresa.com", "tenant_agent")
	const temp = "Temp-Passw0rd!"
	hash, err := password.Hash(temp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(context.Background(), `UPDATE membership_invitations SET temporary_password_hash=$2 WHERE id=$1`, inv.ID, hash); err != nil {
		t.Fatal(err)
	}

	rec, req := asPrincipal(invitee, http.MethodPost, "/x", map[string]string{"token": rawTokenFromURL(t, inv.InviteURL)}, acceptBody(t, temp))
	h.AcceptInvitation(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept with the temporary password = %d %s", rec.Code, rec.Body.String())
	}
	var stored *string
	var mustChange bool
	if err := seed.QueryRow(context.Background(),
		`SELECT password_hash, password_expires_at IS NOT NULL FROM users WHERE id=$1`, invitee).Scan(&stored, &mustChange); err != nil {
		t.Fatal(err)
	}
	if stored == nil || !password.Verify(*stored, temp) || !mustChange {
		t.Fatalf("the temporary password must become the first credential with a forced change (stored=%v mustChange=%v)", stored != nil, mustChange)
	}
}

func TestAcceptWithAWrongPasswordIsRefusedAndChangesNothing(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	invitee := seedUnaffiliatedUser(t, seed, "wrong@empresa.com")
	h := newDevInvitationsHandler(app)
	inv := createInvitation(t, app, h, f, admin, "wrong@empresa.com", "tenant_agent")
	hash, err := password.Hash("Right-Passw0rd!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(context.Background(), `UPDATE membership_invitations SET temporary_password_hash=$2 WHERE id=$1`, inv.ID, hash); err != nil {
		t.Fatal(err)
	}

	rec, req := asPrincipal(invitee, http.MethodPost, "/x", map[string]string{"token": rawTokenFromURL(t, inv.InviteURL)}, acceptBody(t, "Wrong-Passw0rd!"))
	h.AcceptInvitation(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("accept with a wrong password = %d, want 401", rec.Code)
	}
	assertNothingAccepted(t, seed, inv.ID, f.tenantID, invitee)
}

func assertNothingAccepted(t *testing.T, seed *pgxpool.Pool, invID, tenantID, user uuid.UUID) {
	t.Helper()
	var status string
	if err := seed.QueryRow(context.Background(), `SELECT status FROM membership_invitations WHERE id=$1`, invID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("a refused accept consumed the invitation (status=%s)", status)
	}
	var members int
	if err := seed.QueryRow(context.Background(), `SELECT count(*) FROM memberships WHERE tenant_id=$1 AND user_id=$2`, tenantID, user).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 0 {
		t.Fatalf("a refused accept created %d membership(s)", members)
	}
	var hash *string
	if err := seed.QueryRow(context.Background(), `SELECT password_hash FROM users WHERE id=$1`, user).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != nil {
		t.Fatal("a refused accept set a credential")
	}
}
