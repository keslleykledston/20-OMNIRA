package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ADR-0046: the invitation carries the attendance plan and the invitee's sign-in account is prepared before the e-mail goes out.

type fakeLogins struct {
	set      bool
	err      error
	prepared []string
	disabled []string
}

func (f *fakeLogins) Prepare(_ context.Context, email, tempPassword string) (bool, error) {
	if tempPassword == "" {
		return false, errors.New("empty temporary password")
	}
	f.prepared = append(f.prepared, email)
	return f.set, f.err
}

func (f *fakeLogins) Disable(_ context.Context, email string) error {
	f.disabled = append(f.disabled, email)
	return nil
}

func invite(t *testing.T, app *pgxpool.Pool, h *InvitationsHandler, f teamFixture, admin uuid.UUID, req CreateInvitationRequest) (int, Invitation, string) {
	t.Helper()
	body, _ := json.Marshal(req)
	var code int
	var inv Invitation
	var raw string
	if err := asActor(t, app, f.tenantID, admin, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPost, h.CreateInvitation, nil, body)
		code, raw = rec.Code, rec.Body.String()
		if code == http.StatusCreated {
			return json.Unmarshal(rec.Body.Bytes(), &inv)
		}
		return nil
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return code, inv, raw
}

func queueSeats(t *testing.T, seed *pgxpool.Pool, tenantID, userID uuid.UUID) map[uuid.UUID]int {
	t.Helper()
	rows, err := seed.Query(context.Background(), `SELECT queue_id, capacity FROM queue_members WHERE tenant_id=$1 AND user_id=$2 AND active AND available`, tenantID, userID)
	if err != nil {
		t.Fatalf("seats: %v", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]int{}
	for rows.Next() {
		var q uuid.UUID
		var c int
		if err := rows.Scan(&q, &c); err != nil {
			t.Fatalf("seat: %v", err)
		}
		out[q] = c
	}
	return out
}

func agentStatus(t *testing.T, seed *pgxpool.Pool, tenantID, userID uuid.UUID) string {
	t.Helper()
	var status string
	err := seed.QueryRow(context.Background(), `
		SELECT ap.status FROM agent_profiles ap JOIN memberships m ON m.id = ap.membership_id
		WHERE ap.tenant_id=$1 AND m.user_id=$2`, tenantID, userID).Scan(&status)
	if err != nil {
		return ""
	}
	return status
}

func TestInvitationAttendancePlanIsAppliedWhenAccepted(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	support := seedQueue(t, seed, a.tenantID, "Suporte "+uuid.NewString()[:6], "manual", false)
	billing := seedQueue(t, seed, a.tenantID, "Cobrança "+uuid.NewString()[:6], "manual", false)
	gone := seedQueue(t, seed, a.tenantID, "Some "+uuid.NewString()[:6], "manual", false)
	email := "plano-" + uuid.NewString()[:8] + "@empresa.com"
	invitee := seedUnaffiliatedUser(t, seed, email)
	h := newDevInvitationsHandler(app)

	code, inv, body := invite(t, app, h, a, admin, CreateInvitationRequest{
		Email: email, RoleKey: "tenant_agent", Attends: true, QueueIDs: []uuid.UUID{support, billing, gone, support}, QueueCapacity: 3,
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	if !inv.Attends || len(inv.QueueIDs) != 3 || inv.QueueCapacity != 3 {
		t.Fatalf("the response carries the plan without the repeated queue: %+v", inv)
	}
	if _, err := seed.Exec(context.Background(), `DELETE FROM queues WHERE id=$1`, gone); err != nil {
		t.Fatalf("delete queue: %v", err)
	}

	if got := acceptAs(h, invitee, rawTokenFromURL(t, inv.InviteURL)); got != http.StatusOK {
		t.Fatalf("accept = %d", got)
	}
	if s := agentStatus(t, seed, a.tenantID, invitee); s != "active" {
		t.Fatalf("the invitee must start as an active attendant, profile status = %q", s)
	}
	seats := queueSeats(t, seed, a.tenantID, invitee)
	if len(seats) != 2 || seats[support] != 3 || seats[billing] != 3 {
		t.Fatalf("the invitee sits in the chosen queues that still exist, with the chosen capacity: %v", seats)
	}
}

func TestInvitationWithoutAPlanCreatesNoAttendant(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	email := "semplano-" + uuid.NewString()[:8] + "@empresa.com"
	invitee := seedUnaffiliatedUser(t, seed, email)
	h := newDevInvitationsHandler(app)

	code, inv, body := invite(t, app, h, a, admin, CreateInvitationRequest{Email: email, RoleKey: "tenant_agent"})
	if code != http.StatusCreated || inv.Attends || len(inv.QueueIDs) != 0 || inv.QueueCapacity != 1 {
		t.Fatalf("create = %d %s %+v", code, body, inv)
	}
	if got := acceptAs(h, invitee, rawTokenFromURL(t, inv.InviteURL)); got != http.StatusOK {
		t.Fatalf("accept = %d", got)
	}
	if s := agentStatus(t, seed, a.tenantID, invitee); s != "" {
		t.Fatalf("no plan, no attendant profile; got %q", s)
	}
}

func TestInvitationPlanWithoutQueuesUsesTheDefaultOverflowQueue(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	overflow := seedQueue(t, seed, a.tenantID, "Transbordo "+uuid.NewString()[:6], "manual", false)
	if _, err := seed.Exec(context.Background(), `UPDATE tenants SET default_overflow_queue_id=$2 WHERE id=$1`, a.tenantID, overflow); err != nil {
		t.Fatalf("set overflow: %v", err)
	}
	email := "padrao-" + uuid.NewString()[:8] + "@empresa.com"
	invitee := seedUnaffiliatedUser(t, seed, email)
	h := newDevInvitationsHandler(app)

	code, inv, body := invite(t, app, h, a, admin, CreateInvitationRequest{Email: email, RoleKey: "tenant_agent", Attends: true})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	if got := acceptAs(h, invitee, rawTokenFromURL(t, inv.InviteURL)); got != http.StatusOK {
		t.Fatalf("accept = %d", got)
	}
	if seats := queueSeats(t, seed, a.tenantID, invitee); len(seats) != 1 || seats[overflow] == 0 {
		t.Fatalf("like POST /agents, a new attendant with no queue joins the default overflow queue: %v", seats)
	}
}

func TestInvitationPlanRefusesQueuesOfAnotherInstanceAndInconsistentInput(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	b := seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	queueB := seedQueue(t, seed, b.tenantID, "Fila de B "+uuid.NewString()[:6], "manual", false)
	queueA := seedQueue(t, seed, a.tenantID, "Fila de A "+uuid.NewString()[:6], "manual", false)
	h := newDevInvitationsHandler(app)
	email := func() string { return "x-" + uuid.NewString()[:8] + "@empresa.com" }

	cases := []struct {
		name string
		req  CreateInvitationRequest
		want int
	}{
		{"queue of instance B", CreateInvitationRequest{Email: email(), RoleKey: "tenant_agent", Attends: true, QueueIDs: []uuid.UUID{queueA, queueB}}, http.StatusUnprocessableEntity},
		{"queues without attending", CreateInvitationRequest{Email: email(), RoleKey: "tenant_agent", QueueIDs: []uuid.UUID{queueA}}, http.StatusBadRequest},
		{"capacity out of range", CreateInvitationRequest{Email: email(), RoleKey: "tenant_agent", Attends: true, QueueIDs: []uuid.UUID{queueA}, QueueCapacity: 101}, http.StatusBadRequest},
	}
	for _, c := range cases {
		if code, _, body := invite(t, app, h, a, adminA, c.req); code != c.want {
			t.Errorf("%s = %d %s, want %d", c.name, code, body, c.want)
		}
	}
	var n int
	if err := seed.QueryRow(context.Background(), `SELECT count(*) FROM membership_invitations WHERE tenant_id=$1`, a.tenantID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused plan writes no invitation: count=%d err=%v", n, err)
	}
}

func TestInvitationMailsThePasswordOnlyWhenTheSignInAccountTakesIt(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")

	sender, logins := &fakeSender{}, &fakeLogins{set: true}
	h := NewInvitationsHandler(app, nil, sender, false, "https://app.test").WithLoginPreparer(logins)
	if code, _, body := invite(t, app, h, a, admin, CreateInvitationRequest{Email: "nova-" + uuid.NewString()[:8] + "@empresa.com", RoleKey: "tenant_agent"}); code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	if len(sender.sent) != 1 || sender.sent[0].TemporaryPassword == "" {
		t.Fatalf("an account created for the invitation gets its temporary password in the e-mail: %+v", sender.sent)
	}

	logins.set = false
	if code, _, body := invite(t, app, h, a, admin, CreateInvitationRequest{Email: "antiga-" + uuid.NewString()[:8] + "@empresa.com", RoleKey: "tenant_agent"}); code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	if len(sender.sent) != 2 || sender.sent[1].TemporaryPassword != "" {
		t.Fatal("somebody who already has an account is told to use it: no password in the e-mail")
	}

	logins.err = errors.New("keycloak: POST /users answered 500")
	failed := "falhou-" + uuid.NewString()[:8] + "@empresa.com"
	code, _, _ := invite(t, app, h, a, admin, CreateInvitationRequest{Email: failed, RoleKey: "tenant_agent"})
	if code != http.StatusBadGateway {
		t.Fatalf("an account that could not be prepared = %d, want 502 (use resend)", code)
	}
	if len(sender.sent) != 2 {
		t.Fatal("no e-mail goes out with a password the sign-in does not accept")
	}
	var pending bool
	if err := seed.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM membership_invitations WHERE tenant_id=$1 AND email=$2 AND status='pending')`, a.tenantID, failed).Scan(&pending); err != nil || !pending {
		t.Fatalf("the invitation stays pending so it can be resent: %v %v", pending, err)
	}
}

func TestCancellingAnInvitationTurnsOffTheUnusedAccount(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	admin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	logins := &fakeLogins{set: true}
	h := NewInvitationsHandler(app, nil, &fakeSender{}, false, "https://app.test").WithLoginPreparer(logins)
	email := "cancela-" + uuid.NewString()[:8] + "@empresa.com"
	_, inv, _ := invite(t, app, h, a, admin, CreateInvitationRequest{Email: email, RoleKey: "tenant_agent"})

	body, _ := json.Marshal(map[string]string{"status": "revoked"})
	if err := asActor(t, app, a.tenantID, admin, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPatch, h.RevokeInvitation, map[string]string{"invitation_id": inv.ID.String()}, body)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
	if len(logins.disabled) != 1 || logins.disabled[0] != email {
		t.Fatalf("cancelling asks the sign-in side to turn off the unused account: %v", logins.disabled)
	}
}
