package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// IAM2C: e-mail delivery, resend, verified-email acceptance, no silent role change.

const webBase = "https://app.test"

func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func postCreate(t *testing.T, app *pgxpool.Pool, h *InvitationsHandler, f teamFixture, admin uuid.UUID, email, role string) (int, Invitation) {
	t.Helper()
	body, _ := json.Marshal(CreateInvitationRequest{Email: email, RoleKey: role})
	var code int
	var inv Invitation
	if err := asActor(t, app, f.tenantID, admin, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPost, h.CreateInvitation, nil, body)
		code = rec.Code
		if rec.Code == http.StatusCreated {
			return json.Unmarshal(rec.Body.Bytes(), &inv)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
	return code, inv
}

func postResend(t *testing.T, app *pgxpool.Pool, h *InvitationsHandler, tenantID, actor, invitationID uuid.UUID) (int, Invitation) {
	t.Helper()
	var code int
	var inv Invitation
	if err := asActor(t, app, tenantID, actor, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPost, h.ResendInvitation, map[string]string{"invitation_id": invitationID.String()}, nil)
		code = rec.Code
		if rec.Code == http.StatusOK {
			return json.Unmarshal(rec.Body.Bytes(), &inv)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
	return code, inv
}

func sentAtOf(t *testing.T, seed *pgxpool.Pool, id uuid.UUID) *time.Time {
	t.Helper()
	var v *time.Time
	if err := seed.QueryRow(context.Background(), `SELECT sent_at FROM membership_invitations WHERE id=$1`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func acceptAs(h *InvitationsHandler, user uuid.UUID, token string) int {
	rec, req := asPrincipal(user, http.MethodPost, "/api/v1/invitations/"+token+"/accept", map[string]string{"token": token}, nil)
	h.AcceptInvitation(rec, req)
	return rec.Code
}

func TestCreateInvitationDeliversLinkFromWebBaseURL(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	sender := &fakeSender{}
	h := NewInvitationsHandler(app, nil, sender, false, webBase+"/")

	code, inv := postCreate(t, app, h, f, admin, "novo@empresa.com", "tenant_agent")
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected one delivery, got %d", len(sender.sent))
	}
	msg := sender.sent[0]
	if !strings.HasPrefix(msg.AcceptURL, webBase+"/invite/") || strings.Contains(msg.AcceptURL, "//invite") {
		t.Fatalf("accept url must use the web base without double slash: %q", msg.AcceptURL)
	}
	if msg.To != "novo@empresa.com" || msg.TenantName == "" || msg.InviterEmail == "" || msg.ExpiresAt.IsZero() {
		t.Fatalf("incomplete message: %+v", msg)
	}
	raw := tokenFromAcceptURL(t, msg.AcceptURL)
	var stored int
	if err := seed.QueryRow(context.Background(),
		`SELECT count(*) FROM membership_invitations WHERE id=$1 AND token_hash=$2`, inv.ID, tokenHash(raw)).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("only the hash of the emailed token may be stored (count=%d err=%v)", stored, err)
	}
	if err := seed.QueryRow(context.Background(),
		`SELECT count(*) FROM membership_invitations WHERE token_hash=$1`, raw).Scan(&stored); err != nil || stored != 0 {
		t.Fatal("raw token must never be persisted")
	}
	if inv.InviteURL != "" || sentAtOf(t, seed, inv.ID) == nil || inv.SentAt == nil {
		t.Fatalf("production create must not leak invite_url and must record sent_at: %+v", inv)
	}
}

func TestCreateInvitationDeliveryFailureKeepsItPendingAndUnsent(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	h := NewInvitationsHandler(app, nil, &fakeSender{err: errors.New("smtp down")}, false, webBase)

	code, _ := postCreate(t, app, h, f, admin, "falha@empresa.com", "tenant_agent")
	if code != http.StatusBadGateway {
		t.Fatalf("delivery failure must be reported as 502, got %d", code)
	}
	var status string
	var sent *time.Time
	if err := seed.QueryRow(context.Background(),
		`SELECT status, sent_at FROM membership_invitations WHERE tenant_id=$1 AND email='falha@empresa.com'`, f.tenantID).Scan(&status, &sent); err != nil {
		t.Fatalf("invitation should still exist so it can be resent: %v", err)
	}
	if status != "pending" || sent != nil {
		t.Fatalf("status=%s sent_at=%v, want pending with no sent_at", status, sent)
	}
}

func TestCreateInvitationForActiveMemberIsConflict(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	member := seedTeamMember(t, seed, f.tenantID, "tenant_agent", "active")
	var email string
	if err := seed.QueryRow(context.Background(), `SELECT email FROM users WHERE id=$1`, member).Scan(&email); err != nil {
		t.Fatal(err)
	}
	h := NewInvitationsHandler(app, nil, &fakeSender{}, false, webBase)
	if code, _ := postCreate(t, app, h, f, admin, email, "tenant_supervisor"); code != http.StatusConflict {
		t.Fatalf("inviting an active member = %d, want 409", code)
	}
}

func TestResendReissuesTokenAndKillsTheOldLink(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	invitee := seedUnaffiliatedUser(t, seed, "reenvio@empresa.com")
	sender := &fakeSender{}
	// dev=true only so accept does not depend on an IdP; a real sender is still used for delivery.
	h := NewInvitationsHandler(app, nil, sender, true, webBase)

	_, inv := postCreate(t, app, h, f, admin, "reenvio@empresa.com", "tenant_agent")
	oldToken := tokenFromAcceptURL(t, sender.sent[0].AcceptURL)

	// Force expiry: resend must revive an expired-but-pending invitation.
	if _, err := seed.Exec(context.Background(), `UPDATE membership_invitations SET expires_at=now()-interval '1 hour' WHERE id=$1`, inv.ID); err != nil {
		t.Fatal(err)
	}
	code, resent := postResend(t, app, h, f.tenantID, admin, inv.ID)
	if code != http.StatusOK || resent.Status != "pending" || !resent.ExpiresAt.After(time.Now()) || resent.SentAt == nil {
		t.Fatalf("resend = %d %+v", code, resent)
	}
	if len(sender.sent) != 2 {
		t.Fatalf("resend must send a second e-mail, got %d", len(sender.sent))
	}
	newToken := tokenFromAcceptURL(t, sender.sent[1].AcceptURL)
	if newToken == oldToken {
		t.Fatal("resend must issue a new token")
	}
	if c := acceptAs(h, invitee, oldToken); c != http.StatusNotFound {
		t.Fatalf("old link must stop working, got %d", c)
	}
	if c := acceptAs(h, invitee, newToken); c != http.StatusOK {
		t.Fatalf("new link must work, got %d", c)
	}
	if c := acceptAs(h, invitee, newToken); c != http.StatusConflict {
		t.Fatalf("single-use: second accept = %d, want 409", c)
	}
}

func TestResendRules(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f, other := seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	otherAdmin := seedTeamMember(t, seed, other.tenantID, "tenant_admin", "active")
	agent := seedTeamMember(t, seed, f.tenantID, "tenant_agent", "active")
	h := NewInvitationsHandler(app, nil, &fakeSender{}, false, webBase)

	_, revoked := postCreate(t, app, h, f, admin, "revogado@empresa.com", "tenant_agent")
	_, accepted := postCreate(t, app, h, f, admin, "aceito@empresa.com", "tenant_agent")
	_, pending := postCreate(t, app, h, f, admin, "pendente@empresa.com", "tenant_agent")
	if _, err := seed.Exec(context.Background(), `UPDATE membership_invitations SET status='revoked', revoked_at=now() WHERE id=$1`, revoked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(context.Background(), `UPDATE membership_invitations SET status='accepted', accepted_at=now() WHERE id=$1`, accepted.ID); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		tenant, actor, id uuid.UUID
		want              int
	}{
		"revoked is not reopened":       {f.tenantID, admin, revoked.ID, http.StatusConflict},
		"accepted is not reopened":      {f.tenantID, admin, accepted.ID, http.StatusConflict},
		"agent lacks membership.manage": {f.tenantID, agent, pending.ID, http.StatusForbidden},
		"foreign tenant sees nothing":   {other.tenantID, otherAdmin, pending.ID, http.StatusNotFound},
	} {
		if code, _ := postResend(t, app, h, c.tenant, c.actor, c.id); code != c.want {
			t.Errorf("%s: got %d, want %d", name, code, c.want)
		}
	}

	// delivery unavailable fails closed before touching the invitation
	if code, _ := postResend(t, app, newUnavailableInvitationsHandler(app), f.tenantID, admin, pending.ID); code != http.StatusServiceUnavailable {
		t.Errorf("resend without delivery = %d, want 503", code)
	}
}

func TestResendDeliveryFailureStillRotatesAndStaysUnsent(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	sender := &fakeSender{}
	h := NewInvitationsHandler(app, nil, sender, false, webBase)
	_, inv := postCreate(t, app, h, f, admin, "reenvio-falha@empresa.com", "tenant_agent")

	sender.err = errors.New("smtp down")
	if code, _ := postResend(t, app, h, f.tenantID, admin, inv.ID); code != http.StatusBadGateway {
		t.Fatalf("resend failure = %d, want 502", code)
	}
	if sentAtOf(t, seed, inv.ID) != nil {
		t.Fatal("a failed resend must leave sent_at empty")
	}
	sender.err = nil
	if code, r := postResend(t, app, h, f.tenantID, admin, inv.ID); code != http.StatusOK || r.SentAt == nil {
		t.Fatalf("second resend should succeed: %d %+v", code, r)
	}
}

func TestAcceptPreservesTheRoleOfAnAlreadyActiveMember(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	invitee := seedUnaffiliatedUser(t, seed, "ja-admin@empresa.com")
	sender := &fakeSender{}
	h := NewInvitationsHandler(app, nil, sender, true, webBase)

	postCreate(t, app, h, f, admin, "ja-admin@empresa.com", "tenant_agent")
	token := tokenFromAcceptURL(t, sender.sent[0].AcceptURL)
	// The invitee becomes an active admin AFTER the invitation was issued (race with PATCH /team).
	var roleID uuid.UUID
	if err := seed.QueryRow(context.Background(), `SELECT id FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(context.Background(),
		`INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,'active')`,
		uuid.New(), f.tenantID, invitee, roleID); err != nil {
		t.Fatal(err)
	}
	if c := acceptAs(h, invitee, token); c != http.StatusOK {
		t.Fatalf("accept = %d", c)
	}
	var key string
	if err := seed.QueryRow(context.Background(), `SELECT r.key FROM memberships m JOIN roles r ON r.id=m.role_id WHERE m.tenant_id=$1 AND m.user_id=$2`,
		f.tenantID, invitee).Scan(&key); err != nil || key != "tenant_admin" {
		t.Fatalf("an invitation must not downgrade an active member: role=%q err=%v", key, err)
	}
}

func TestAcceptRequiresAnIdPVerifiedEmailOutsideDev(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	invitee := seedUnaffiliatedUser(t, seed, "verificar@empresa.com")
	sender := &fakeSender{}
	h := NewInvitationsHandler(app, nil, sender, false, webBase)
	postCreate(t, app, h, f, admin, "verificar@empresa.com", "tenant_agent")
	token := tokenFromAcceptURL(t, sender.sent[0].AcceptURL)

	statusOf := func() string {
		rec, req := asPrincipal(invitee, http.MethodGet, "/api/v1/invitations/"+token+"/status", map[string]string{"token": token}, nil)
		h.InvitationStatus(rec, req)
		var body struct{ Status string }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return body.Status
	}
	addIdentity := func(email string, verified bool) {
		if _, err := seed.Exec(context.Background(),
			`INSERT INTO user_identities(user_id, issuer, subject, email, email_verified) VALUES ($1,$2,$3,$4,$5)`,
			invitee, "https://idp.test/"+uuid.NewString(), uuid.NewString(), email, verified); err != nil {
			t.Fatal(err)
		}
	}

	// no IdP evidence at all
	if c := acceptAs(h, invitee, token); c != http.StatusForbidden || statusOf() != "email_unverified" {
		t.Fatalf("no identity: accept=%d status=%q", c, statusOf())
	}
	// IdP says the address is NOT verified
	addIdentity("verificar@empresa.com", false)
	if c := acceptAs(h, invitee, token); c != http.StatusForbidden {
		t.Fatalf("unverified identity: accept=%d, want 403", c)
	}
	// verified, but for a different address than the invitation
	addIdentity("outro@empresa.com", true)
	if c := acceptAs(h, invitee, token); c != http.StatusForbidden {
		t.Fatalf("verified other address: accept=%d, want 403", c)
	}
	// verified for the invited address
	addIdentity("Verificar@Empresa.com", true)
	if statusOf() != "pending" {
		t.Fatalf("status with verified email = %q, want pending", statusOf())
	}
	if c := acceptAs(h, invitee, token); c != http.StatusOK {
		t.Fatalf("verified accept = %d, want 200", c)
	}
	var st string
	if err := seed.QueryRow(context.Background(), `SELECT status FROM memberships WHERE tenant_id=$1 AND user_id=$2`, f.tenantID, invitee).Scan(&st); err != nil || st != "active" {
		t.Fatalf("membership not activated: %q %v", st, err)
	}
}

// ADR-0039: a company administrator invites people into THEIR company; someone who already works in another instance is the
// Hub administrator's to authorize.
func TestInvitationRefusesSomeoneWhoAlreadyWorksInAnotherInstance(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a, b := seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	h := NewInvitationsHandler(app, nil, &fakeSender{}, false, webBase)
	emailOf := func(u uuid.UUID) string {
		var e string
		if err := seed.QueryRow(context.Background(), `SELECT email FROM users WHERE id=$1`, u).Scan(&e); err != nil {
			t.Fatal(err)
		}
		return e
	}

	// works (active) in B -> refused, and nothing was created
	inB := seedTeamMember(t, seed, b.tenantID, "tenant_agent", "active")
	before := func() int {
		var n int
		_ = seed.QueryRow(context.Background(), `SELECT count(*) FROM membership_invitations WHERE tenant_id=$1`, a.tenantID).Scan(&n)
		return n
	}
	n0 := before()
	if code, _ := postCreate(t, app, h, a, adminA, emailOf(inB), "tenant_agent"); code != http.StatusConflict {
		t.Fatalf("inviting someone who works in another instance = %d, want 409", code)
	}
	if before() != n0 {
		t.Fatalf("a refused invitation was stored")
	}
	// an INACTIVE membership elsewhere does not count
	gone := seedTeamMember(t, seed, b.tenantID, "tenant_agent", "inactive")
	if code, _ := postCreate(t, app, h, a, adminA, emailOf(gone), "tenant_agent"); code != http.StatusCreated {
		t.Fatalf("a former member of another instance can be invited: %d", code)
	}
	// being an agent of a hub counts
	hubPerson := seedUnaffiliatedUser(t, seed, "agente-do-hub@empresa.com")
	hubID := uuid.New()
	if _, err := seed.Exec(context.Background(), `INSERT INTO service_hubs (id, name) VALUES ($1, 'H')`, hubID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM service_hubs WHERE id=$1`, hubID) })
	if _, err := seed.Exec(context.Background(), `INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, (SELECT id FROM roles WHERE key='hub_agent' AND tenant_id IS NULL))`, hubID, hubPerson); err != nil {
		t.Fatal(err)
	}
	if code, _ := postCreate(t, app, h, a, adminA, "agente-do-hub@empresa.com", "tenant_agent"); code != http.StatusConflict {
		t.Fatalf("inviting a hub agent = %d, want 409", code)
	}
	// somebody who works nowhere is the normal case
	seedUnaffiliatedUser(t, seed, "novo@empresa.com")
	code, inv := postCreate(t, app, h, a, adminA, "novo@empresa.com", "tenant_agent")
	if code != http.StatusCreated {
		t.Fatalf("a person who works nowhere: %d", code)
	}
	// ...until they start working elsewhere: resending the old invitation is refused too
	newcomer := seedUnaffiliatedUser(t, seed, "novo2@empresa.com")
	_, inv2 := postCreate(t, app, h, a, adminA, "novo2@empresa.com", "tenant_agent")
	var role uuid.UUID
	if err := seed.QueryRow(context.Background(), `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(context.Background(), `INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,'active')`, uuid.New(), b.tenantID, newcomer, role); err != nil {
		t.Fatal(err)
	}
	if c, _ := postResend(t, app, h, a.tenantID, adminA, inv2.ID); c != http.StatusConflict {
		t.Fatalf("resend after the person joined another instance = %d, want 409", c)
	}
	_ = inv

	// the function is no oracle: someone who cannot manage A's people always hears "no"
	var answer bool
	if err := platformdb.WithTenantSession(context.Background(), app, inB, false, func(ctx context.Context) error {
		return platformdb.QuerierFromContext(ctx, app).QueryRow(ctx, `SELECT person_works_in_other_instance($1, $2)`, a.tenantID, emailOf(inB)).Scan(&answer)
	}); err != nil {
		t.Fatal(err)
	}
	if answer {
		t.Fatalf("a caller who cannot manage the company's people learned who works elsewhere")
	}
	// "elsewhere" means ANOTHER instance: a colleague who works only here is not elsewhere
	colleague := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	var asked bool
	if err := asActor(t, app, a.tenantID, adminA, func(ctx context.Context) error {
		return platformdb.QuerierFromContext(ctx, app).QueryRow(ctx, `SELECT person_works_in_other_instance($1, $2)`, a.tenantID, emailOf(colleague)).Scan(&asked)
	}); err != nil {
		t.Fatal(err)
	}
	if asked {
		t.Fatalf("someone who works only in this company was reported as working elsewhere")
	}
	// a plain colleague of the company (member of A, but without membership.manage) learns nothing either
	if err := asActor(t, app, a.tenantID, colleague, func(ctx context.Context) error {
		return platformdb.QuerierFromContext(ctx, app).QueryRow(ctx, `SELECT person_works_in_other_instance($1, $2)`, a.tenantID, emailOf(inB)).Scan(&asked)
	}); err != nil {
		t.Fatal(err)
	}
	if asked {
		t.Fatalf("a member without membership.manage learned who works in another instance")
	}
	// the company's own administrator does get the real answer
	if err := asActor(t, app, a.tenantID, adminA, func(ctx context.Context) error {
		return platformdb.QuerierFromContext(ctx, app).QueryRow(ctx, `SELECT person_works_in_other_instance($1, $2)`, a.tenantID, emailOf(inB)).Scan(&asked)
	}); err != nil {
		t.Fatal(err)
	}
	if !asked {
		t.Fatalf("the company's administrator was not told that the person works in another instance")
	}
}
