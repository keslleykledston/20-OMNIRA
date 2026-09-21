package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/platform/authn"
)

// seedUnaffiliatedUser cria um usuário com e-mail conhecido e nenhuma
// membership em tenant nenhum — o estado real de quem ainda vai aceitar um
// convite.
func seedUnaffiliatedUser(t *testing.T, pool *pgxpool.Pool, email string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO users (id, external_subject, email, status) VALUES ($1,$2,$3,'active')`,
		id, id.String(), email); err != nil {
		t.Fatalf("seed unaffiliated user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id)
	})
	return id
}

// fakeSender simula um provedor de e-mail real configurado (não é
// NoopInvitationSender), então deliveryAvailable fica true mesmo com
// devExposeInviteURL=false — o cenário "produção com sender configurado".
type fakeSender struct{ sent []string }

func (f *fakeSender) Send(_ context.Context, email, _, _ string) error {
	f.sent = append(f.sent, email)
	return nil
}

// newInvitationsHandler simula produção com delivery configurada: sender real,
// devExposeInviteURL=false. Cobre os testes que não são especificamente sobre
// a gate de delivery (permissão, escopo por tenant, duplicidade, revogação).
func newInvitationsHandler(app *pgxpool.Pool) *InvitationsHandler {
	return NewInvitationsHandler(app, nil, &fakeSender{}, false, "https://app.test")
}

// newUnavailableInvitationsHandler simula produção sem nenhuma forma de
// entrega: NoopInvitationSender e sem dev auth. CreateInvitation deve
// recusar antes de qualquer escrita.
func newUnavailableInvitationsHandler(app *pgxpool.Pool) *InvitationsHandler {
	return NewInvitationsHandler(app, nil, nil, false, "https://app.test")
}

func asPrincipal(userID uuid.UUID, method, target string, pathValues map[string]string, body []byte) (*httptest.ResponseRecorder, *http.Request) {
	ctx := authn.WithPrincipal(context.Background(), &authn.Principal{UserID: userID})
	req := httptest.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	return httptest.NewRecorder(), req
}

func createInvitation(t *testing.T, app *pgxpool.Pool, h *InvitationsHandler, f teamFixture, admin uuid.UUID, email, roleKey string) Invitation {
	t.Helper()
	body, _ := json.Marshal(CreateInvitationRequest{Email: email, RoleKey: roleKey})
	var inv Invitation
	if err := asActor(t, app, f.tenantID, admin, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPost, h.CreateInvitation, nil, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create invitation = %d %s", rec.Code, rec.Body.String())
		}
		return json.Unmarshal(rec.Body.Bytes(), &inv)
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return inv
}

// tokenFor extracts the raw token generated for an invitation by intercepting
// the InviteURL that dev-mode exposure returns — tests build handlers with
// devExposeInviteURL=true specifically to get this without touching the DB
// column directly (which only stores the hash, on purpose).
func newDevInvitationsHandler(app *pgxpool.Pool) *InvitationsHandler {
	return NewInvitationsHandler(app, nil, nil, true, "https://app.test")
}

func rawTokenFromURL(t *testing.T, invitePath string) string {
	t.Helper()
	const prefix = "/invite/"
	if len(invitePath) <= len(prefix) {
		t.Fatalf("unexpected invite path: %s", invitePath)
	}
	return invitePath[len(prefix):]
}

func TestCreateInvitationByAdmin(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	h := newInvitationsHandler(app)

	inv := createInvitation(t, app, h, f, admin, "new-hire@empresa.com", "tenant_agent")
	if inv.Status != "pending" || inv.RoleKey != "tenant_agent" {
		t.Fatalf("unexpected invitation: %+v", inv)
	}
	if inv.InviteURL != "" {
		t.Fatal("invite_url must not leak when dev exposure is off")
	}
}

func TestCreateInvitationDeniedForAgent(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	agent := seedTeamMember(t, seed, f.tenantID, "tenant_agent", "active")
	h := newInvitationsHandler(app)

	body, _ := json.Marshal(CreateInvitationRequest{Email: "x@y.com", RoleKey: "tenant_agent"})
	if err := asActor(t, app, f.tenantID, agent, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPost, h.CreateInvitation, nil, body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("agent create invitation = %d, want 403", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

func TestSupervisorCanListButNotCreateInvitations(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	supervisor := seedTeamMember(t, seed, f.tenantID, "tenant_supervisor", "active")
	h := newInvitationsHandler(app)

	if err := asActor(t, app, f.tenantID, supervisor, func(ctx context.Context) error {
		listRec := doRequest(t, ctx, http.MethodGet, h.ListInvitations, nil, nil)
		if listRec.Code != http.StatusOK {
			t.Fatalf("supervisor list = %d, want 200", listRec.Code)
		}
		body, _ := json.Marshal(CreateInvitationRequest{Email: "x@y.com", RoleKey: "tenant_agent"})
		createRec := doRequest(t, ctx, http.MethodPost, h.CreateInvitation, nil, body)
		if createRec.Code != http.StatusForbidden {
			t.Fatalf("supervisor create = %d, want 403", createRec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

func TestCannotInviteToGlobalRole(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	h := newInvitationsHandler(app)

	body, _ := json.Marshal(CreateInvitationRequest{Email: "x@y.com", RoleKey: "system_admin"})
	if err := asActor(t, app, f.tenantID, admin, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPost, h.CreateInvitation, nil, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invite to system_admin = %d, want 422", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

func TestListInvitationsIsScopedToTenant(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	b := seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	adminB := seedTeamMember(t, seed, b.tenantID, "tenant_admin", "active")
	h := newInvitationsHandler(app)

	createInvitation(t, app, h, a, adminA, "a-invite@empresa.com", "tenant_agent")
	createInvitation(t, app, h, b, adminB, "b-invite@empresa.com", "tenant_agent")

	if err := asActor(t, app, a.tenantID, adminA, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodGet, h.ListInvitations, nil, nil)
		var body struct {
			Items []Invitation `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			return err
		}
		if len(body.Items) != 1 || body.Items[0].Email != "a-invite@empresa.com" {
			t.Fatalf("tenant A should see only its own invitation, got %+v", body.Items)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

func TestDuplicatePendingInvitationReplacesThePrevious(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	h := newInvitationsHandler(app)

	first := createInvitation(t, app, h, f, admin, "dup@empresa.com", "tenant_agent")
	second := createInvitation(t, app, h, f, admin, "dup@empresa.com", "tenant_supervisor")

	if err := asActor(t, app, f.tenantID, admin, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodGet, h.ListInvitations, nil, nil)
		var body struct {
			Items []Invitation `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			return err
		}
		var firstStatus, secondStatus string
		for _, i := range body.Items {
			if i.ID == first.ID {
				firstStatus = i.Status
			}
			if i.ID == second.ID {
				secondStatus = i.Status
			}
		}
		if firstStatus != "revoked" {
			t.Fatalf("previous pending invitation should be revoked, got %s", firstStatus)
		}
		if secondStatus != "pending" {
			t.Fatalf("new invitation should be pending, got %s", secondStatus)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

func TestRevokeInvitationOfForeignTenantIs404(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	b := seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	adminB := seedTeamMember(t, seed, b.tenantID, "tenant_admin", "active")
	h := newInvitationsHandler(app)

	invB := createInvitation(t, app, h, b, adminB, "b@empresa.com", "tenant_agent")

	body, _ := json.Marshal(map[string]string{"status": "revoked"})
	if err := asActor(t, app, a.tenantID, adminA, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPatch, h.RevokeInvitation,
			map[string]string{"invitation_id": invB.ID.String()}, body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("cross-tenant revoke = %d, want 404", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}

	var status string
	if err := seed.QueryRow(context.Background(),
		`SELECT status FROM membership_invitations WHERE id=$1`, invB.ID).Scan(&status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "pending" {
		t.Fatal("tenant B's invitation was mutated by tenant A's revoke attempt")
	}
}

func TestAcceptInvitationCreatesMembership(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	invitee := seedUnaffiliatedUser(t, seed, "invitee@empresa.com")
	hDev := newDevInvitationsHandler(app)

	inv := createInvitation(t, app, hDev, f, admin, "invitee@empresa.com", "tenant_agent")
	token := rawTokenFromURL(t, inv.InviteURL)

	rec, req := asPrincipal(invitee, http.MethodPost, "/api/v1/invitations/"+token+"/accept", map[string]string{"token": token}, nil)
	hDev.AcceptInvitation(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept = %d %s", rec.Code, rec.Body.String())
	}

	var roleKey, status string
	if err := seed.QueryRow(context.Background(), `
		SELECT r.key, m.status FROM memberships m JOIN roles r ON r.id=m.role_id
		WHERE m.tenant_id=$1 AND m.user_id=$2`, f.tenantID, invitee).Scan(&roleKey, &status); err != nil {
		t.Fatalf("membership not created: %v", err)
	}
	if roleKey != "tenant_agent" || status != "active" {
		t.Fatalf("membership = role=%s status=%s", roleKey, status)
	}
}

func TestAcceptInvitationTwiceIsRejectedSecondTime(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	invitee := seedUnaffiliatedUser(t, seed, "reused@empresa.com")
	hDev := newDevInvitationsHandler(app)

	inv := createInvitation(t, app, hDev, f, admin, "reused@empresa.com", "tenant_agent")
	token := rawTokenFromURL(t, inv.InviteURL)

	rec1, req1 := asPrincipal(invitee, http.MethodPost, "/x", map[string]string{"token": token}, nil)
	hDev.AcceptInvitation(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first accept = %d", rec1.Code)
	}

	rec2, req2 := asPrincipal(invitee, http.MethodPost, "/x", map[string]string{"token": token}, nil)
	hDev.AcceptInvitation(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second accept = %d, want 409", rec2.Code)
	}

	var count int
	if err := seed.QueryRow(context.Background(),
		`SELECT count(*) FROM memberships WHERE tenant_id=$1 AND user_id=$2`, f.tenantID, invitee).Scan(&count); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one membership, got %d", count)
	}
}

func TestAcceptRevokedInvitationIsRejected(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	invitee := seedUnaffiliatedUser(t, seed, "revoked-invitee@empresa.com")
	hDev := newDevInvitationsHandler(app)

	inv := createInvitation(t, app, hDev, f, admin, "revoked-invitee@empresa.com", "tenant_agent")
	token := rawTokenFromURL(t, inv.InviteURL)

	body, _ := json.Marshal(map[string]string{"status": "revoked"})
	if err := asActor(t, app, f.tenantID, admin, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPatch, hDev.RevokeInvitation,
			map[string]string{"invitation_id": inv.ID.String()}, body)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
		}
		return nil
	}); err != nil {
		t.Fatalf("revoke session: %v", err)
	}

	rec, req := asPrincipal(invitee, http.MethodPost, "/x", map[string]string{"token": token}, nil)
	hDev.AcceptInvitation(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("accept revoked = %d, want 409", rec.Code)
	}
}

func TestAcceptExpiredInvitationIsRejected(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	invitee := seedUnaffiliatedUser(t, seed, "expired-invitee@empresa.com")
	hDev := newDevInvitationsHandler(app)

	inv := createInvitation(t, app, hDev, f, admin, "expired-invitee@empresa.com", "tenant_agent")
	token := rawTokenFromURL(t, inv.InviteURL)

	// Backdate expires_at directly — the only way to exercise "expired"
	// deterministically without waiting 72h.
	if _, err := seed.Exec(context.Background(),
		`UPDATE membership_invitations SET expires_at = now() - interval '1 minute' WHERE id=$1`, inv.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	rec, req := asPrincipal(invitee, http.MethodPost, "/x", map[string]string{"token": token}, nil)
	hDev.AcceptInvitation(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("accept expired = %d, want 410", rec.Code)
	}
}

func TestAcceptWithWrongIdentityIsRejected(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	_ = seedUnaffiliatedUser(t, seed, "intended@empresa.com")
	wrongUser := seedUnaffiliatedUser(t, seed, "attacker@empresa.com")
	hDev := newDevInvitationsHandler(app)

	inv := createInvitation(t, app, hDev, f, admin, "intended@empresa.com", "tenant_agent")
	token := rawTokenFromURL(t, inv.InviteURL)

	rec, req := asPrincipal(wrongUser, http.MethodPost, "/x", map[string]string{"token": token}, nil)
	hDev.AcceptInvitation(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("accept with wrong identity = %d, want 404", rec.Code)
	}

	var count int
	if err := seed.QueryRow(context.Background(),
		`SELECT count(*) FROM memberships WHERE tenant_id=$1 AND user_id=$2`, f.tenantID, wrongUser).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatal("wrong identity was granted a membership")
	}
}

func TestInvitationStatusDistinguishesStates(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	invitee := seedUnaffiliatedUser(t, seed, "status-check@empresa.com")
	wrongUser := seedUnaffiliatedUser(t, seed, "someone-else@empresa.com")
	hDev := newDevInvitationsHandler(app)

	inv := createInvitation(t, app, hDev, f, admin, "status-check@empresa.com", "tenant_agent")
	token := rawTokenFromURL(t, inv.InviteURL)

	statusAs := func(userID uuid.UUID) string {
		rec, req := asPrincipal(userID, http.MethodGet, "/x", map[string]string{"token": token}, nil)
		hDev.InvitationStatus(rec, req)
		var body struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		return body.Status
	}

	if got := statusAs(invitee); got != "pending" {
		t.Fatalf("status for invitee = %s, want pending", got)
	}
	if got := statusAs(wrongUser); got != "wrong_identity" {
		t.Fatalf("status for wrong user = %s, want wrong_identity", got)
	}

	rec, req := asPrincipal(invitee, http.MethodGet, "/x", map[string]string{"token": "not-a-real-token"}, nil)
	hDev.InvitationStatus(rec, req)
	var body struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Status != "not_found" {
		t.Fatalf("status for bogus token = %s, want not_found", body.Status)
	}
}

func TestInvitationsTableEnforcesRLS(t *testing.T) {
	app := teamAppPool(t)
	var rls, force bool
	if err := app.QueryRow(context.Background(),
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname='membership_invitations'`).
		Scan(&rls, &force); err != nil {
		t.Fatalf("inspect table: %v", err)
	}
	if !rls || !force {
		t.Fatalf("membership_invitations must have RLS and FORCE RLS, got rls=%v force=%v", rls, force)
	}
}

// Sanity: the invitation TTL used by CreateInvitation is what expiresAt below
// assumes; if this ever changes, the expired-invitation test's manual
// backdate remains valid regardless, but this pins the constant's magnitude.
func TestInvitationTTLIsSeventyTwoHours(t *testing.T) {
	if invitationTTL != 72*time.Hour {
		t.Fatalf("invitationTTL = %v, want 72h", invitationTTL)
	}
}

// --- Delivery fail-closed (human gate follow-up) ---

func TestCreateInvitationRejectedWhenDeliveryUnavailable(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	h := newUnavailableInvitationsHandler(app)

	body, _ := json.Marshal(CreateInvitationRequest{Email: "sem-entrega@empresa.com", RoleKey: "tenant_agent"})
	if err := asActor(t, app, f.tenantID, admin, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPost, h.CreateInvitation, nil, body)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("create without delivery = %d, want 503", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}

	var count int
	if err := seed.QueryRow(context.Background(),
		`SELECT count(*) FROM membership_invitations WHERE tenant_id=$1`, f.tenantID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("invitation was persisted despite unavailable delivery: %d rows", count)
	}
}

// A permission check ainda vem primeiro: a gate de delivery não deve revelar
// nada a quem não teria permissão de convidar de qualquer forma.
func TestDeliveryUnavailableDoesNotBypassPermissionCheck(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	agent := seedTeamMember(t, seed, f.tenantID, "tenant_agent", "active")
	h := newUnavailableInvitationsHandler(app)

	body, _ := json.Marshal(CreateInvitationRequest{Email: "x@y.com", RoleKey: "tenant_agent"})
	if err := asActor(t, app, f.tenantID, agent, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPost, h.CreateInvitation, nil, body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("agent create (delivery also unavailable) = %d, want 403", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

func TestCreateInvitationSucceedsWithConfiguredSender(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	sender := &fakeSender{}
	h := NewInvitationsHandler(app, nil, sender, false, "https://app.test")

	inv := createInvitation(t, app, h, f, admin, "entregue@empresa.com", "tenant_agent")
	if inv.Status != "pending" {
		t.Fatalf("unexpected invitation: %+v", inv)
	}
	if inv.InviteURL != "" {
		t.Fatal("invite_url must not leak in production even with a real sender")
	}
	if len(sender.sent) != 1 || sender.sent[0] != "entregue@empresa.com" {
		t.Fatalf("sender was not invoked correctly: %+v", sender.sent)
	}
}

func TestCreateInvitationSucceedsInDevWithoutSender(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	f := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, f.tenantID, "tenant_admin", "active")
	hDev := newDevInvitationsHandler(app)

	inv := createInvitation(t, app, hDev, f, admin, "dev-sem-sender@empresa.com", "tenant_agent")
	if inv.InviteURL == "" {
		t.Fatal("dev auth active must expose the relative invite_url")
	}
}

func TestInvitationDeliveryAvailableReflectsConfiguration(t *testing.T) {
	if NewInvitationsHandler(nil, nil, nil, false, "").InvitationDeliveryAvailable() {
		t.Fatal("Noop sender + no dev auth must not be available")
	}
	if !NewInvitationsHandler(nil, nil, nil, true, "").InvitationDeliveryAvailable() {
		t.Fatal("dev auth active must be available regardless of sender")
	}
	if !NewInvitationsHandler(nil, nil, &fakeSender{}, false, "").InvitationDeliveryAvailable() {
		t.Fatal("a real sender must be available regardless of dev auth")
	}
}
